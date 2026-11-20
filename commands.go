package main

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/spf13/cobra"

	"github.com/tulayha/closedvps/internal/buy"
	"github.com/tulayha/closedvps/internal/cloud"
	"github.com/tulayha/closedvps/internal/tsapi"
)

func (a *app) printJSON(v any) error {
	e := json.NewEncoder(a.stdout)
	e.SetIndent("", "  ")
	return e.Encode(v)
}

func newTypesCmd(a *app) *cobra.Command {
	var locations string
	var all, asJSON bool
	cmd := &cobra.Command{
		Use:   "types",
		Short: "List server types, prices and what's in stock now",
		Long: `Lists server types, monthly price with IPv4 (before VAT) and whether Hetzner says they're in stock.
Reading needs only a Read token.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.types(cmd.Context(), splitList(locations), all, asJSON)
		},
	}
	cmd.Flags().StringVar(&locations, "location", "", "only these locations, comma-separated")
	cmd.Flags().BoolVar(&all, "all", false, "include dedicated-vCPU types")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

type typeRow struct {
	buy.Candidate
	Currency string `json:"currency"`
	InStock  bool   `json:"in_stock"`
}

func (a *app) types(ctx context.Context, locs []string, all, asJSON bool) error {
	api, err := a.connect()
	if err != nil {
		return err
	}
	types, err := api.ServerTypes(ctx)
	if err != nil {
		return err
	}
	prices, err := api.Prices(ctx)
	if err != nil {
		return err
	}
	if len(locs) == 0 {
		for _, t := range types {
			for _, l := range t.Locations {
				if l.Location != nil && !slices.Contains(locs, l.Location.Name) {
					locs = append(locs, l.Location.Name)
				}
			}
		}
	}
	w := buy.Wants{Locations: locs}
	if all {
		for _, t := range types {
			w.Types = append(w.Types, t.Name)
		}
	}
	cands := w.Candidates(types, prices.IPv4)
	slices.SortStableFunc(cands, func(x, y buy.Candidate) int { return cmp.Compare(x.Price, y.Price) })
	avail := buy.Availability(types)
	rows := make([]typeRow, 0, len(cands))
	for _, c := range cands {
		rows = append(rows, typeRow{c, prices.Currency, avail[c.String()]})
	}
	if asJSON {
		return a.printJSON(rows)
	}
	tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TYPE\tLOCATION\tCORES\tMEMORY\tARCH\tPER MONTH\tIN STOCK")
	for _, r := range rows {
		stock := "no"
		if r.InStock {
			stock = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\t%s\n", r.Type, r.Location, r.Cores, gb(r.Memory), r.Arch, buy.Money(r.Price, prices.Currency), stock)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(a.stdout, "\nIn stock is Hetzner's own hint for everyone; your account can still be refused, which `create` retries.")
	return nil
}

func newListCmd(a *app) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Every server in this Hetzner project: price, whether the internet can reach it, and your tailnet",
		Long: "Shows every server in this Hetzner project, made by closedvps or not: its monthly price, whether its " +
			"firewalls let anything in from the internet, and, when this computer runs Tailscale or a Tailscale OAuth " +
			"client is saved, whether it's on your tailnet. A server with no firewall takes connections on every port " +
			"it listens on. It also names closedvps firewalls left without a server. --json has the firewall rules.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return a.list(cmd.Context(), asJSON) },
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

type serverRow struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Location string  `json:"location"`
	Status   string  `json:"status"`
	IPv4     string  `json:"ipv4"`
	Price    float64 `json:"price"`
	Currency string  `json:"currency"`
	Internet string  `json:"internet"` // "closed", or "open: " and what's let in
	Firewall string  `json:"firewall"` // "closed", or the rules that let traffic in
	Tailnet  string  `json:"tailnet,omitempty"`
	Managed  bool    `json:"closedvps"`
}

type listing struct {
	Servers   []serverRow `json:"servers"`
	Leftovers []string    `json:"leftover_firewalls"`
}

// services names the ports people know by name.
var services = map[string]string{"tcp 22": "SSH", "tcp 80": "HTTP", "tcp 443": "HTTPS", "icmp": "ping"}

// internet sums up what a server's firewalls let in, the way list shows it.
func internet(s *hcloud.Server, rules []hcloud.FirewallRule, firewalls int) string {
	if cloud.IPv4(s) == "" && (s.PublicNet.IPv6.IP == nil || s.PublicNet.IPv6.IP.IsUnspecified()) {
		return "closed, no public IP"
	}
	if firewalls == 0 {
		return "open: every port"
	}
	var what []string
	for _, r := range rules {
		k := string(r.Protocol)
		if r.Port != nil {
			k += " " + *r.Port
		}
		if n, ok := services[k]; ok {
			k = n
		}
		if !slices.Contains(what, k) {
			what = append(what, k)
		}
	}
	if len(what) == 0 {
		return "closed"
	}
	return "open: " + strings.Join(what, ", ")
}

// tailnetState is how list sees each server on the tailnet: from this computer's Tailscale, or the saved
// OAuth client, or not at all.
func (a *app) tailnetState(ctx context.Context) func(s *hcloud.Server) string {
	if bin := a.tailscale(); bin != "" {
		if peers, err := a.peers(ctx, bin); err == nil {
			return func(s *hcloud.Server) string {
				// Any machine by that name, also one that joined before the server was made: a server set up
				// by hand, or this computer.
				p := findPeer(peers, s.Name, time.Time{})
				if p == nil {
					return "not joined"
				}
				state := "offline"
				if p.Online {
					state = "online"
				}
				if slices.Contains(p.Tags, tsapi.Tag) {
					state += ", tagged"
				}
				return state
			}
		}
	}
	if id, secret, _ := tsCredentials(); id != "" {
		if ts, err := tsapi.New(id, secret, a.tsBase, "closedvps/"+currentVersion()); err == nil {
			if ds, err := ts.Devices(ctx); err == nil {
				return func(s *hcloud.Server) string {
					for _, d := range ds {
						if peerName(d.Name, s.Name) {
							state := "offline"
							if d.Online {
								state = "online"
							}
							if d.Tagged {
								state += ", tagged"
							}
							return state
						}
					}
					return "not joined"
				}
			}
		}
	}
	return nil
}

func (a *app) list(ctx context.Context, asJSON bool) error {
	api, err := a.connect()
	if err != nil {
		return err
	}
	servers, err := api.Servers(ctx)
	if err != nil {
		return err
	}
	types, err := api.ServerTypes(ctx)
	if err != nil {
		return err
	}
	prices, err := api.Prices(ctx)
	if err != nil {
		return err
	}
	leftovers, err := api.Leftovers(ctx)
	if err != nil {
		return err
	}
	tailnet := a.tailnetState(ctx)
	out := listing{Servers: []serverRow{}, Leftovers: []string{}}
	for _, fw := range leftovers {
		out.Leftovers = append(out.Leftovers, fw.Name)
	}
	for _, s := range servers {
		state, err := api.Exposure(ctx, s)
		if err != nil {
			return err
		}
		rules, n, err := api.Inbound(ctx, s)
		if err != nil {
			return err
		}
		row := serverRow{
			Name: s.Name, Status: string(s.Status), IPv4: cloud.IPv4(s), Firewall: state,
			Internet: internet(s, rules, n), Managed: cloud.IsManaged(s), Currency: currencyOr(prices.Currency),
		}
		if s.Location != nil {
			row.Location = s.Location.Name
		}
		if s.ServerType != nil {
			row.Type = s.ServerType.Name
			for _, t := range types {
				if t.Name == row.Type {
					row.Price, _ = buy.Monthly(t, row.Location, prices.IPv4, s.BackupWindow != "")
				}
			}
		}
		if tailnet != nil {
			row.Tailnet = tailnet(s)
		}
		out.Servers = append(out.Servers, row)
	}
	if asJSON {
		return a.printJSON(out)
	}
	u := newUI(a.stderr, false, a.now)
	if len(out.Servers) == 0 {
		u.info("No servers in this project. Make one with closedvps create")
	} else if err := a.serverTable(out.Servers, tailnet != nil); err != nil {
		return err
	}
	first := true
	gap := func() {
		if first && len(out.Servers) > 0 {
			u.line("")
		}
		first = false
	}
	for _, r := range out.Servers {
		switch {
		case strings.HasPrefix(r.Firewall, "MISSING"):
			gap()
			u.warn("%s has lost its closedvps firewall %s", r.Name, cloud.FirewallName(r.Name))
			u.hint("Attach it again in the Hetzner Console, or delete the server with closedvps delete %s", r.Name)
		case r.Internet == "open: every port":
			gap()
			u.warn("%s has no firewall, so it takes connections on every port it listens on", r.Name)
			u.hint("To close it without locking yourself out: closedvps.com/guide#close-a-server-you-already-have")
		case strings.HasPrefix(r.Internet, "open: "):
			gap()
			what := strings.Split(strings.TrimPrefix(r.Internet, "open: "), ", ")
			if n := len(what); n > 1 {
				what = append(what[:n-2], what[n-2]+" and "+what[n-1])
			}
			u.warn("%s takes %s from the internet", r.Name, strings.Join(what, ", "))
			u.hint("To close it without locking yourself out: closedvps.com/guide#close-a-server-you-already-have")
		}
	}
	for _, fw := range out.Leftovers {
		gap()
		name := strings.TrimPrefix(fw, "closedvps-")
		u.info("Firewall %s has no server; closedvps delete %s removes it", fw, name)
	}
	return nil
}

// serverTable writes the servers aligned for a person, or tab-separated when stdout isn't a terminal.
func (a *app) serverTable(rows []serverRow, withTailnet bool) error {
	cur := rows[0].Currency
	head := []string{"NAME", "TYPE", "WHERE", cur + "/MO", "INTERNET"}
	if withTailnet {
		head = append(head, "TAILNET")
	}
	w := a.stdout
	var tw *tabwriter.Writer
	if a.watched {
		tw = tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
		w = tw
	}
	fmt.Fprintln(w, strings.Join(head, "\t"))
	for _, r := range rows {
		cells := []string{r.Name, r.Type, r.Location, fmt.Sprintf("%.2f", r.Price), r.Internet}
		if withTailnet {
			cells = append(cells, r.Tailnet)
		}
		fmt.Fprintln(w, strings.Join(cells, "\t"))
	}
	if tw != nil {
		return tw.Flush()
	}
	return nil
}

func newDeleteCmd(a *app) *cobra.Command {
	var confirm string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "delete <name>...",
		Short: "Delete servers closedvps made, with their firewalls",
		Long: `Deletes servers closedvps made, with their disks, backups and closedvps firewalls. Hetzner bills by the
hour, so the bill stops at once. With a name that has no server, it offers to delete the closedvps firewall
left behind under that name. Servers closedvps didn't make are never deleted.

It asks you to type the name (or, for several, how many) to confirm. Without a terminal, pass that same
answer with --confirm.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return a.delete(cmd.Context(), args, confirm, asJSON) },
	}
	cmd.Flags().StringVar(&confirm, "confirm", "", "the name of the server to delete (for several, how many), so it doesn't ask")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print one JSON event per deletion on stdout")
	return cmd
}

func (a *app) delete(ctx context.Context, names []string, confirm string, asJSON bool) error {
	api, err := a.connect()
	if err != nil {
		return err
	}
	rep := &reporter{now: a.now}
	if asJSON {
		rep.json = json.NewEncoder(a.stdout)
	}
	u := newUI(a.stderr, false, a.now)
	prices, err := api.Prices(ctx)
	if err != nil {
		return err
	}
	types, err := api.ServerTypes(ctx)
	if err != nil {
		return err
	}
	type target struct {
		s     *hcloud.Server
		fw    *hcloud.Firewall
		price float64
	}
	var targets []target
	var orphans []*hcloud.Firewall
	for _, n := range names {
		s, err := api.ServerByName(ctx, n)
		if err != nil {
			return err
		}
		fw, _, err := api.Firewall.GetByName(ctx, cloud.FirewallName(n))
		if err != nil {
			return err
		}
		switch {
		case s == nil && fw != nil && cloud.IsManagedFirewall(fw) && len(fw.AppliedTo) == 0:
			orphans = append(orphans, fw)
		case s == nil:
			return fail("not_found", "No server or closedvps firewall named "+n, "closedvps list shows what's there.")
		case !cloud.IsManaged(s):
			return fail("not_closedvps", n+" wasn't made by closedvps, so it won't be deleted", "Delete it in the Hetzner Console if you mean to.")
		default:
			t := target{s: s}
			if fw != nil && cloud.IsManagedFirewall(fw) {
				t.fw = fw
			}
			for _, ty := range types {
				if s.ServerType != nil && ty.Name == s.ServerType.Name && s.Location != nil {
					t.price, _ = buy.Monthly(ty, s.Location.Name, prices.IPv4, s.BackupWindow != "")
				}
			}
			targets = append(targets, t)
		}
	}
	cur := currencyOr(prices.Currency)

	if len(targets) > 0 {
		var total float64
		for _, t := range targets {
			total += t.price
			line := fmt.Sprintf("%s · %s · %.2f %s/mo", t.s.ServerType.Name, t.s.Location.Name, t.price, cur)
			if len(targets) == 1 {
				line += " · made by closedvps " + t.s.Created.Format("2006-01-02")
			}
			u.line("  %s  %s", t.s.Name, line)
		}
		want := targets[0].s.Name
		if len(targets) == 1 {
			what := "Deletes the server, its disk and backups"
			if targets[0].fw != nil {
				what += ", and firewall " + targets[0].fw.Name
			}
			u.line("  %s.", what)
		} else {
			want = strconv.Itoa(len(targets))
			how := strconv.Itoa(len(targets))
			if len(targets) == 2 {
				how = "both"
			}
			u.line("  Deletes %s servers, their disks, backups and firewalls (%.2f %s/mo).", how, total, cur)
		}
		if confirm == "" {
			if !a.tty {
				return fail("needs_yes", "Not deleting without a confirmation", "Pass --confirm "+want+".")
			}
			ans, err := a.text("Type "+want+" to confirm:", "", func(string) error { return nil })
			if err != nil {
				return err
			}
			confirm = ans
		}
		if strings.TrimSpace(confirm) != want {
			return fail("declined", "That doesn't match, so nothing was deleted", "Type "+want+" to delete.")
		}
		var deleted []string
		for _, t := range targets {
			snaps, _ := api.Snapshots(ctx, t.s.ID)
			if err := api.DeleteServer(ctx, t.s); err != nil {
				return fmt.Errorf("deleting %s: %w", t.s.Name, err)
			}
			ev := event{Event: "deleted", Server: t.s.Name, Price: t.price, Currency: cur}
			if t.fw != nil {
				if err := a.deleteFirewall(ctx, api, t.fw); err != nil {
					u.warn("Deleted %s, but not its firewall %s: %v", t.s.Name, t.fw.Name, err)
					u.hint("closedvps delete %s removes it later.", t.s.Name)
				} else {
					ev.Firewall = t.fw.Name
				}
			}
			rep.emit(ev)
			deleted = append(deleted, t.s.Name)
			if len(targets) == 1 {
				if ev.Firewall != "" {
					u.ok("Deleted %s and firewall %s", t.s.Name, ev.Firewall)
				} else {
					u.ok("Deleted %s", t.s.Name)
				}
			}
			if len(snaps) > 0 {
				var gb float64
				for _, i := range snaps {
					gb += float64(i.ImageSize)
				}
				u.warn("%s left %s (%.2f %s/mo); delete them in the Hetzner Console", t.s.Name,
					plural(len(snaps), "1 snapshot", fmt.Sprintf("%d snapshots", len(snaps))), gb*prices.SnapshotGB, cur)
			}
		}
		if len(targets) > 1 {
			u.ok("Deleted %s and their firewalls", strings.Join(deleted, ", "))
		}
		u.info("Remove %s from your tailnet too: login.tailscale.com/admin/machines", plural(len(targets), "it", "them"))
	}

	for _, fw := range orphans {
		name := strings.TrimPrefix(fw.Name, "closedvps-")
		u.info("No server named %s, but its firewall %s is still there", name, fw.Name)
		ok := strings.TrimSpace(confirm) == name
		if !ok && !a.tty {
			return fail("needs_yes", "Not deleting firewall "+fw.Name+" without a confirmation", "Pass --confirm "+name+".")
		}
		if !ok && !a.yesNo("Delete firewall "+fw.Name+"?", false) {
			continue
		}
		if err := a.deleteFirewall(ctx, api, fw); err != nil {
			return fmt.Errorf("deleting firewall %s: %w", fw.Name, err)
		}
		rep.emit(event{Event: "deleted", Firewall: fw.Name})
		u.ok("Deleted firewall %s", fw.Name)
	}
	return nil
}

// deleteFirewall deletes a firewall, trying again for a while: Hetzner takes a moment to let go of a
// firewall whose server was just deleted.
func (a *app) deleteFirewall(ctx context.Context, api *cloud.Client, fw *hcloud.Firewall) error {
	var err error
	for range 10 {
		if _, err = api.Firewall.Delete(ctx, fw); err == nil || !cloud.IsCode(err, "resource_in_use") {
			return err
		}
		if err := a.sleep(ctx, 3*time.Second); err != nil {
			return err
		}
	}
	return err
}

func newOpenSSHCmd(a *app) *cobra.Command {
	var from string
	cmd := &cobra.Command{
		Use:   "open-ssh <name>",
		Short: "Let SSH in from your current IP only (when Tailscale is broken)",
		Long: `For when Tailscale is broken: adds one firewall rule that lets SSH (TCP 22) in from a single IP address, so
you can ssh to the server's public address and fix it. Close it again with: closedvps close-ssh <name>`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return a.openSSH(cmd.Context(), args[0], from) },
	}
	cmd.Flags().StringVar(&from, "from", "", "IP address to let in (default: this computer's public IPv4 address)")
	return cmd
}

func (a *app) openSSH(ctx context.Context, name, from string) error {
	api, err := a.connect()
	if err != nil {
		return err
	}
	srv, fw, err := api.ServerFirewall(ctx, name)
	if err != nil {
		return err
	}
	ip := from
	if ip == "" {
		if ip, err = a.publicIP(ctx); err != nil {
			return fmt.Errorf("finding your public IP address: %w; pass it with --from", err)
		}
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return fmt.Errorf("%q is not an IP address", ip)
	}
	bits := 128
	if parsed.To4() != nil {
		parsed, bits = parsed.To4(), 32
	}
	rules := append(slices.Clone(fw.Rules), hcloud.FirewallRule{
		Direction: hcloud.FirewallRuleDirectionIn, Protocol: hcloud.FirewallRuleProtocolTCP, Port: new("22"),
		SourceIPs:   []net.IPNet{{IP: parsed, Mask: net.CIDRMask(bits, bits)}},
		Description: new("closedvps open-ssh " + a.now().Format("2006-01-02 15:04")),
	})
	if err := api.SetRules(ctx, fw, rules); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "SSH is open to %s only. Connect with: ssh <your user>@%s\n", ip, cloud.IPv4(srv))
	fmt.Fprintf(a.stdout, "Close it when you're done: closedvps close-ssh %s\n", name)
	return nil
}

func newCloseSSHCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "close-ssh <name>",
		Short: "Close what open-ssh opened",
		Long:  "Removes every inbound rule from the server's firewall, so nothing can reach it from the internet again.",
		Args:  cobra.ExactArgs(1),
		RunE:  func(cmd *cobra.Command, args []string) error { return a.closeSSH(cmd.Context(), args[0]) },
	}
}

func (a *app) closeSSH(ctx context.Context, name string) error {
	api, err := a.connect()
	if err != nil {
		return err
	}
	srv, fw, err := api.ServerFirewall(ctx, name)
	if err != nil {
		return err
	}
	var keep []hcloud.FirewallRule
	for _, r := range fw.Rules {
		if r.Direction != hcloud.FirewallRuleDirectionIn {
			keep = append(keep, r)
		}
	}
	if err := api.SetRules(ctx, fw, keep); err != nil {
		return err
	}
	open, err := api.OpenFirewalls(ctx, srv.ID)
	if err != nil {
		return err
	}
	if len(open) > 0 {
		return fmt.Errorf("%s is closed, but other firewalls on %s let traffic in, which closedvps didn't make: %s. Remove them in the Hetzner Console",
			cloud.FirewallName(name), name, strings.Join(open, "; "))
	}
	fmt.Fprintf(a.stdout, "%s is closed: no firewall on it lets anything in.\n", name)
	return nil
}

// publicIP asks a plain what's-my-IP service; it sees only that this computer made a request.
func (a *app) publicIP(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://ipv4.icanhazip.com", nil)
	if err != nil {
		return "", err
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
