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
	"strings"
	"text/tabwriter"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/spf13/cobra"

	"github.com/tulayha/closedvps/internal/buy"
	"github.com/tulayha/closedvps/internal/cloud"
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
		fmt.Fprintf(tw, "%s\t%s\t%d\t%.0f GB\t%s\t%s\t%s\n", r.Type, r.Location, r.Cores, r.Memory, r.Arch, buy.Money(r.Price, prices.Currency), stock)
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
		Short: "Every server in this Hetzner project and whether the internet can reach it",
		Long: "Shows every server in this Hetzner project, made by closedvps or not, and whether its firewalls let " +
			"anything in from the internet. A server with no firewall takes connections on every port it listens on.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return a.list(cmd.Context(), asJSON) },
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

type serverRow struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Location string `json:"location"`
	Status   string `json:"status"`
	IPv4     string `json:"ipv4"`
	Firewall string `json:"firewall"` // "closed", or what's wrong
	Managed  bool   `json:"closedvps"`
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
	rows := make([]serverRow, 0, len(servers))
	for _, s := range servers {
		state, err := api.Exposure(ctx, s)
		if err != nil {
			return err
		}
		row := serverRow{Name: s.Name, Status: string(s.Status), IPv4: cloud.IPv4(s), Firewall: state, Managed: cloud.IsManaged(s)}
		if s.ServerType != nil {
			row.Type = s.ServerType.Name
		}
		if s.Location != nil {
			row.Location = s.Location.Name
		}
		rows = append(rows, row)
	}
	if asJSON {
		return a.printJSON(rows)
	}
	if len(rows) == 0 {
		fmt.Fprintln(a.stdout, "No servers in this project. Make one with: closedvps create")
		return nil
	}
	open := false
	for _, r := range rows {
		open = open || strings.HasPrefix(r.Firewall, "OPEN")
	}
	tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTYPE\tLOCATION\tSTATUS\tIPV4\tFIREWALL")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Name, r.Type, r.Location, r.Status, r.IPv4, r.Firewall)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if open {
		fmt.Fprintln(a.stdout, "\nOPEN servers take connections from the internet. closedvps.com/guide#close-a-server-you-already-have shows how to shut them without locking yourself out.")
	}
	return nil
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
