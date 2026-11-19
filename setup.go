package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"charm.land/huh/v2"
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

// text asks for one line, starting from def. Enter keeps def; check sees what was typed, never "".
func (a *app) text(title, def string, check func(string) error) (string, error) {
	v := def
	in := huh.NewInput().Title(title).Value(&v).Validate(func(s string) error {
		if strings.TrimSpace(s) == "" {
			return nil // Enter alone keeps the default
		}
		return check(strings.TrimSpace(s))
	})
	if err := a.question(in, func() string { return title + " " + cmp.Or(strings.TrimSpace(v), def) }); err != nil {
		return "", err
	}
	if v = strings.TrimSpace(v); v == "" {
		return def, nil
	}
	// huh's plain prompts hand back the last answer, valid or not, when the input ends.
	return v, check(v)
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
	ans, err := a.text("Locations, numbers or names in the order to try them:", strings.Join(p.Locations, ","), func(s string) error {
		_, err := pickLocations(s, locs)
		return err
	})
	if err != nil {
		return p, err
	}
	if p.Locations, err = pickLocations(ans, locs); err != nil {
		return p, err
	}

	fmt.Fprintln(out, "\nWhich server types? Cheapest shared ones in those locations (per month, before VAT, IPv4 included):")
	avail := buy.Availability(types)
	seen := map[string]bool{}
	w = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  TYPE\tCORES\tMEMORY\tARCH\tFROM\tIN STOCK NOW")
	for _, c := range (buy.Wants{Locations: p.Locations}).Candidates(types, prices.IPv4) {
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
	fmt.Fprintln(out, "  `closedvps types --all` lists every type, dedicated ones included. A type can carry its own")
	fmt.Fprintln(out, "  monthly cap, like cx23:6,cx33:12, and the others share the cap asked next. \"any\" tries the")
	fmt.Fprintln(out, "  cheapest shared type in stock.")
	known := map[string]bool{}
	for _, t := range types {
		known[t.Name] = true
	}
	def := strings.Join(p.Types, ",")
	if def == "" {
		def = "any"
	}
	ans, err = a.text("Types in the order to try them:", def, func(s string) error {
		if strings.EqualFold(s, "any") {
			return nil
		}
		names, _, err := buy.ParseTypes(splitList(s))
		if err != nil {
			return err
		}
		for _, t := range names {
			if !known[t] {
				return fmt.Errorf("Hetzner has no type %q", t)
			}
		}
		return nil
	})
	if err != nil {
		return p, err
	}
	if p.Types = splitList(ans); strings.EqualFold(ans, "any") {
		p.Types = nil
	}

	fmt.Fprintln(out, "\nHetzner Backups keep 7 daily copies of the whole server for 20% of its price.")
	if err := a.question(huh.NewConfirm().Title("Turn on backups?").Value(&p.Backups), func() string {
		return "Turn on backups? " + map[bool]string{true: "Yes", false: "No"}[p.Backups]
	}); err != nil {
		return p, err
	}

	what := "its IPv4"
	if p.Backups {
		what = "its IPv4 and backups"
	}
	ans, err = a.text(fmt.Sprintf("Most you'll pay per month for one server with %s, in %s before VAT:", what, currencyOr(prices.Currency)),
		strconv.FormatFloat(p.MaxPrice, 'f', -1, 64), func(s string) error {
			if _, ok := parseMoney(s); !ok {
				return errors.New("a number above 0, please")
			}
			return nil
		})
	if err != nil {
		return p, err
	}
	p.MaxPrice, _ = parseMoney(ans)
	names, caps, _ := buy.ParseTypes(p.Types)
	wants := buy.Wants{Types: names, Caps: caps, Locations: p.Locations, MaxPrice: p.MaxPrice, Backups: p.Backups}
	if len(wants.Candidates(types, prices.IPv4)) == 0 {
		fmt.Fprintln(out, "  Nothing on your list costs that little, so `closedvps create` won't find anything. Run setup again with a higher cap.")
	}

	def = "none"
	if p.MaxTotal > 0 {
		def = strconv.FormatFloat(p.MaxTotal, 'f', -1, 64)
	}
	ans, err = a.text("Most all your closedvps servers in this Hetzner project may cost together per month, or \"none\":", def, func(s string) error {
		if v, ok := parseMoney(s); strings.EqualFold(s, "none") || (ok && v >= p.MaxPrice) {
			return nil
		}
		return fmt.Errorf("a number of at least %s (one server), or none", strconv.FormatFloat(p.MaxPrice, 'f', -1, 64))
	})
	if err != nil {
		return p, err
	}
	p.MaxTotal, _ = parseMoney(ans) // "none" reads as 0, no limit

	fmt.Fprintln(out)
	if p.User, err = a.text("Linux user name on the server:", p.User, func(s string) error {
		if !cloudinit.ValidUser(s) {
			return errors.New("lowercase letters, digits, _ and -, starting with a letter, and not a system name like root")
		}
		return nil
	}); err != nil {
		return p, err
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
// "None" deletes saved links; a step that fails leaves them as they were.
func (a *app) setupAlerts(ctx context.Context) error {
	out := a.stderr
	fmt.Fprintln(out, "\nWaiting for stock can take hours. This computer shows a notification when a server is bought and")
	fmt.Fprintln(out, "when it's ready. Messages name the server, type and price; the \"ready\" one comes from the new server.")
	const keep, ntfy, telegram, links, none = "keep", "ntfy", "telegram", "links", "none"
	var opts []huh.Option[string]
	choice := none
	if saved, err := alert.Parse(linksStore.get()); err == nil && len(saved) > 0 {
		var n []string
		for _, l := range saved {
			n = append(n, l.Name())
		}
		opts = append(opts, huh.NewOption("Keep: "+strings.Join(n, ", "), keep))
		choice = keep
	}
	opts = append(opts,
		huh.NewOption("ntfy app on your phone (a new private topic)", ntfy),
		huh.NewOption("Telegram (a bot you make with @BotFather)", telegram),
		huh.NewOption("Links: Discord or Slack webhooks, ntfy topics, any https webhook", links),
		huh.NewOption("None, this computer only", none),
	)
	const title = "Send alerts to your phone or a chat too?"
	if err := a.question(huh.NewSelect[string]().Title(title).Options(opts...).Value(&choice), func() string {
		i := slices.IndexFunc(opts, func(o huh.Option[string]) bool { return o.Value == choice })
		return title + " " + opts[i].Key
	}); err != nil {
		return err
	}

	var link, tip string
	switch choice {
	case keep:
		return nil
	case none:
		_, err := linksStore.forget()
		return err
	case ntfy:
		link = newNtfyLink()
		tip = "In the ntfy app, tap +, and subscribe to " + strings.TrimPrefix(link, "https://ntfy.sh/") + ". Anyone who knows that name can read the messages."
	case telegram:
		var err error
		if link, err = a.connectTelegram(ctx); err != nil {
			return err
		}
	case links:
		var err error
		if link, err = a.text("Links, comma-separated:", "", func(s string) error {
			_, err := alert.Parse(s)
			return err
		}); err != nil {
			return err
		}
	}
	parsed, err := alert.Parse(link)
	if err != nil {
		return err
	}
	if len(parsed) == 0 {
		return errors.New("no alert links given; the saved ones are unchanged")
	}
	where, err := linksStore.save(link)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "  Saved in %s.\n", where)
	if tip != "" {
		fmt.Fprintln(out, " ", tip)
		if err := a.question(huh.NewInput().Title("Press Enter once you've subscribed, for a test message."), nil); err != nil {
			return err
		}
	}
	errs := alert.Send(ctx, a.http, parsed, alert.Message{Title: "closedvps test alert", Text: "Alerts from closedvps will arrive here."})
	for _, e := range errs {
		fmt.Fprintf(out, "  Test message failed for %v\n", e)
	}
	if len(errs) < len(parsed) {
		fmt.Fprintln(out, "  Sent a test message. If it didn't arrive, run `closedvps setup` again.")
	}
	return nil
}

func (a *app) connectTelegram(ctx context.Context) (string, error) {
	fmt.Fprintln(a.stderr, "  In Telegram, message @BotFather with /newbot and copy the token it gives you.")
	var token string
	in := huh.NewInput().Title("Bot token:").EchoMode(huh.EchoModePassword).Value(&token).Validate(func(s string) error {
		if strings.TrimSpace(s) == "" {
			return errors.New("paste the token")
		}
		return nil
	})
	if err := a.question(in, nil); err != nil {
		return "", err
	}
	if err := a.question(huh.NewInput().Title("Now send any message to your new bot in Telegram, then press Enter here."), nil); err != nil {
		return "", err
	}
	chat, err := alert.TelegramChat(ctx, a.http, strings.TrimSpace(token))
	if err != nil {
		return "", err
	}
	return alert.TelegramLink(strings.TrimSpace(token), chat), nil
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
