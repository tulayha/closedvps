package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/spf13/cobra"

	"github.com/tulayha/closedvps/internal/alert"
	"github.com/tulayha/closedvps/internal/buy"
	"github.com/tulayha/closedvps/internal/cloud"
	"github.com/tulayha/closedvps/internal/cloudinit"
)

func newSetupCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Change your saved locations, types, price caps, backups and alerts",
		Long: `Asks which locations, server types, price caps, backups and alerts you want, and saves the answers as
the defaults for ` + "`closedvps create`" + `. Run it again to change them.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !a.tty {
				return errors.New("setup asks questions, so run it in a terminal")
			}
			api, err := a.connect()
			if err != nil {
				return err
			}
			p, _, err := loadPrefs()
			if err != nil {
				return err
			}
			_, err = a.setupWizard(cmd.Context(), api, p)
			return err
		},
	}
}

// setupWizard walks through the preferences with live prices and stock, saves them and returns them.
func (a *app) setupWizard(ctx context.Context, api *cloud.Client, p prefs) (prefs, error) {
	types, err := api.ServerTypes(ctx)
	if err != nil {
		return p, err
	}
	prices, err := api.Prices(ctx)
	if err != nil {
		return p, err
	}
	locs, err := api.Locations(ctx)
	if err != nil {
		return p, err
	}
	out := a.stderr
	fmt.Fprintln(out, "\nA few questions, asked once. The answers become the defaults for `closedvps create`; run `closedvps setup` to change them.")

	fmt.Fprintln(out, "\nWhere should the server be? Hetzner's locations:")
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for i, l := range locs {
		fmt.Fprintf(w, "  %d\t%s\t%s, %s\n", i+1, l.Name, l.City, l.Country)
	}
	_ = w.Flush()
	for {
		chosen, err := pickLocations(a.ask("Numbers or names in the order to try them, comma-separated:", strings.Join(p.Locations, ",")), locs)
		if err == nil {
			p.Locations = chosen
			break
		}
		fmt.Fprintln(out, " ", err)
	}

	fmt.Fprintln(out, "\nWhich server types? Cheapest shared ones in those locations (per month, before VAT, IPv4 included):")
	all := buy.Wants{Locations: p.Locations}.Candidates(types, prices.IPv4)
	seen := map[string]bool{}
	avail := buy.Availability(types)
	w = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  TYPE\tCORES\tMEMORY\tARCH\tFROM\tIN STOCK NOW")
	for _, c := range all {
		if seen[c.Type] || len(seen) == 10 {
			continue
		}
		seen[c.Type] = true
		var in []string
		for _, l := range p.Locations {
			if avail[c.Type+" "+l] {
				in = append(in, l)
			}
		}
		stock := "nowhere"
		if len(in) > 0 {
			stock = strings.Join(in, ", ")
		}
		fmt.Fprintf(w, "  %s\t%d\t%.0f GB\t%s\t%s\t%s\n", c.Type, c.Cores, c.Memory, c.Arch, buy.Money(c.Price, prices.Currency), stock)
	}
	_ = w.Flush()
	fmt.Fprintln(out, "  `closedvps types --all` lists every type, dedicated ones included.")
	known := map[string]bool{}
	for _, t := range types {
		known[t.Name] = true
	}
	fmt.Fprintln(out, "  A type can carry its own monthly cap, like cx23:6,cx33:12; the others share the cap asked next.")
	for {
		def := strings.Join(p.Types, ",")
		if def == "" {
			def = "any"
		}
		ans := a.ask("Types in the order to try them, or \"any\" for the cheapest shared type in stock:", def)
		if strings.EqualFold(ans, "any") {
			p.Types = nil
			break
		}
		list := splitList(ans)
		names, _, err := buy.ParseTypes(list)
		if err != nil {
			fmt.Fprintln(out, " ", err)
			continue
		}
		bad := ""
		for _, t := range names {
			if !known[t] {
				bad = t
			}
		}
		if bad == "" && len(names) > 0 {
			p.Types = list
			break
		}
		fmt.Fprintf(out, "  Hetzner has no type %q.\n", bad)
	}

	fmt.Fprintln(out, "\nHetzner Backups keep 7 daily copies of the whole server for 20% of its price.")
	p.Backups = a.confirm("Turn on backups?", p.Backups)

	for {
		what := "server and IPv4"
		if p.Backups {
			what = "server, IPv4 and backups"
		}
		ans := a.ask(fmt.Sprintf("Most you'll pay per month for each server's %s, in %s before VAT:", what, currencyOr(prices.Currency)), strconv.FormatFloat(p.MaxPrice, 'f', -1, 64))
		if v, ok := parseMoney(ans); ok {
			p.MaxPrice = v
			break
		}
		fmt.Fprintln(out, "  A number above 0, please.")
	}
	names, caps, _ := buy.ParseTypes(p.Types)
	wants := buy.Wants{Types: names, Caps: caps, Locations: p.Locations, MaxPrice: p.MaxPrice, Backups: p.Backups}
	if len(wants.Candidates(types, prices.IPv4)) == 0 {
		fmt.Fprintln(out, "  Nothing on your list costs that little, so `closedvps create` won't find anything. Run setup again with a higher cap.")
	}
	for {
		def := "none"
		if p.MaxTotal > 0 {
			def = strconv.FormatFloat(p.MaxTotal, 'f', -1, 64)
		}
		ans := a.ask("Most all your closedvps servers in this Hetzner project may cost together per month, or \"none\":", def)
		if strings.EqualFold(ans, "none") {
			p.MaxTotal = 0
			break
		}
		if v, ok := parseMoney(ans); ok && v >= p.MaxPrice {
			p.MaxTotal = v
			break
		}
		fmt.Fprintf(out, "  A number of at least %s (one server), or none.\n", strconv.FormatFloat(p.MaxPrice, 'f', -1, 64))
	}

	for {
		u := a.ask("\nLinux user name on the server:", p.User)
		if cloudinit.ValidUser(u) {
			p.User = u
			break
		}
		fmt.Fprintln(out, "  Lowercase letters, digits, _ and -, starting with a letter, and not a system name like root.")
	}

	if err := a.setupAlerts(ctx); err != nil {
		return p, err
	}

	path, err := savePrefs(p)
	if err != nil {
		return p, err
	}
	fmt.Fprintf(out, "\nSaved to %s.\n\n", path)
	return p, nil
}

// parseMoney reads an amount, with a comma or a dot for the decimals.
func parseMoney(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(s), ",", "."), 64)
	return v, err == nil && buy.ValidPrice(v)
}

// setupAlerts asks where remote alerts go, saves the links as a secret and sends a test message. Only
// "none" deletes saved links; an answer with a step that failed is asked again.
func (a *app) setupAlerts(ctx context.Context) error {
	out := a.stderr
	saved := linksStore.get()
	fmt.Fprintln(out, "\nWaiting for stock can take hours. This computer shows a notification when a server is bought and")
	fmt.Fprintln(out, "when it's ready. To get those on your phone or in a chat too, give one or more of these, comma-separated:")
	fmt.Fprintln(out, "  ntfy              a new private topic in the free ntfy app")
	fmt.Fprintln(out, "  telegram          connects a Telegram bot you make with @BotFather")
	fmt.Fprintln(out, "  a link            a Discord or Slack webhook, an ntfy topic, or any https webhook")
	fmt.Fprintln(out, "  none              alerts stay on this computer")
	fmt.Fprintln(out, "Messages name the server, type and price. The \"ready\" one is sent by the new server itself.")
	def := "none"
	if saved != "" {
		def = "keep"
		if links, err := alert.Parse(saved); err == nil {
			var n []string
			for _, l := range links {
				n = append(n, l.Name())
			}
			fmt.Fprintf(out, "Now: %s.\n", strings.Join(n, ", "))
		}
	}
ask:
	for {
		ans := strings.TrimSpace(a.ask("Alerts:", def))
		switch strings.ToLower(ans) {
		case "keep":
			return nil
		case "none", "":
			_, err := linksStore.forget()
			return err
		}
		var parts, tips []string
		for _, part := range strings.Split(ans, ",") {
			switch part = strings.TrimSpace(part); strings.ToLower(part) {
			case "":
			case "ntfy":
				l := newNtfyLink()
				parts = append(parts, l)
				tips = append(tips, "In the ntfy app, tap +, and subscribe to "+strings.TrimPrefix(l, "https://ntfy.sh/")+". Anyone who knows that name can read the messages.")
			case "telegram":
				l, err := a.connectTelegram(ctx)
				if err != nil {
					fmt.Fprintln(out, " ", err)
					if !a.tty {
						return err
					}
					continue ask
				}
				parts = append(parts, l)
			default:
				parts = append(parts, part)
			}
		}
		joined := strings.Join(parts, ",")
		links, err := alert.Parse(joined)
		if err != nil {
			fmt.Fprintln(out, " ", err)
			if !a.tty {
				return err
			}
			continue
		}
		for _, t := range tips {
			fmt.Fprintln(out, " ", t)
		}
		where, err := linksStore.save(joined)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "  Saved in %s.\n", where)
		if len(tips) > 0 {
			a.ask("Press Enter once you've subscribed, for a test message.", "")
		}
		errs := alert.Send(ctx, a.http, links, alert.Message{Title: "closedvps test alert", Text: "Alerts from closedvps will arrive here."})
		for _, e := range errs {
			fmt.Fprintf(out, "  Test message failed for %v\n", e)
		}
		if len(errs) < len(links) {
			fmt.Fprintln(out, "  Sent a test message. If it didn't arrive, run `closedvps setup` again.")
		}
		return nil
	}
}

func (a *app) connectTelegram(ctx context.Context) (string, error) {
	fmt.Fprintln(a.stderr, "  In Telegram, message @BotFather with /newbot and copy the token it gives you.")
	token, err := a.askSecret("  Bot token:")
	if err != nil {
		return "", err
	}
	a.ask("  Now send any message to your new bot in Telegram, then press Enter here.", "")
	chat, err := alert.TelegramChat(ctx, a.http, token)
	if err != nil {
		return "", err
	}
	return alert.TelegramLink(token, chat), nil
}

func pickLocations(ans string, locs []*hcloud.Location) ([]string, error) {
	var out []string
	for _, l := range splitList(ans) {
		name := l
		if n, err := strconv.Atoi(l); err == nil {
			if n < 1 || n > len(locs) {
				return nil, fmt.Errorf("there's no location %d", n)
			}
			name = locs[n-1].Name
		} else if !slices.ContainsFunc(locs, func(x *hcloud.Location) bool { return x.Name == l }) {
			return nil, fmt.Errorf("there's no location %q", l)
		}
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("pick at least one")
	}
	return out, nil
}

func currencyOr(c string) string {
	if c == "" {
		return "EUR"
	}
	return c
}

func newForgetCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "forget",
		Short: "Delete the saved Hetzner token and alert links from this computer",
		Long: `Deletes the saved Hetzner token and alert links from this computer. Revoke the token in the Hetzner
Console too if you're done with it.`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			for _, s := range []store{tokenStore, linksStore} {
				removed, err := s.forget()
				if err != nil {
					return err
				}
				if removed {
					fmt.Fprintf(a.stdout, "Deleted the saved %s.\n", s.what)
				} else {
					fmt.Fprintf(a.stdout, "No saved %s on this computer.\n", s.what)
				}
			}
			return nil
		},
	}
}
