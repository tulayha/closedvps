package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"charm.land/huh/v2"
	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/spf13/cobra"

	"github.com/tulayha/closedvps/internal/alert"
	"github.com/tulayha/closedvps/internal/buy"
	"github.com/tulayha/closedvps/internal/cloud"
	"github.com/tulayha/closedvps/internal/cloudinit"
	"github.com/tulayha/closedvps/internal/tsapi"
)

func newSetupCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Save your defaults: what servers are for, locations, types, caps, alerts and Tailscale",
		Long: `Asks what your servers are usually for, which locations, server types, price caps, backups and alerts you
want, and optionally saves a Tailscale OAuth client. The answers become the defaults for ` + "`closedvps create`" + `,
and its flags change them for one run. Nobody has to run setup: without it, create uses defaults and asks
only what the server is for. Run it again to change the answers.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !a.tty {
				return fail("usage", "setup asks questions, so run it in a terminal", "closedvps create takes every answer as a flag.")
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
	if err := a.question(in, func() string { return cyan.Render("?") + " " + title + " " + cmp.Or(strings.TrimSpace(v), def) }); err != nil {
		return "", err
	}
	if v = strings.TrimSpace(v); v == "" {
		return def, nil
	}
	// huh's plain prompts hand back the last answer, valid or not, when the input ends.
	return v, check(v)
}

// setupWizard walks through the preferences with live prices and stock, saves them and returns them. What
// the servers are for comes first and the cap before the types, so the type list only shows what fits.
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
	u := newUI(a.stderr, false, a.now)
	cur := currencyOr(prices.Currency)

	choice := slices.IndexFunc(purposes, func(x purpose) bool { return x.claude == p.Claude && x.codex == p.Codex })
	if err := a.choose("What are your servers usually for?", purposeLabels(), &choice); err != nil {
		return p, err
	}
	p.Claude, p.Codex = purposes[choice].claude, purposes[choice].codex
	var minMemory float64
	if p.Claude || p.Codex {
		minMemory = agentMemory
	}

	var where []string
	for i, l := range locs {
		where = append(where, fmt.Sprintf("%d %s %s", i+1, l.Name, l.City))
	}
	u.hint("%s", faint.Render("Hetzner's locations: "+strings.Join(where, ", ")))
	ans, err := a.text("Locations, in order:", strings.Join(p.Locations, ", "), func(s string) error {
		_, err := pickLocations(s, locs)
		return err
	})
	if err != nil {
		return p, err
	}
	if p.Locations, err = pickLocations(ans, locs); err != nil {
		return p, err
	}

	p.Backups = a.yesNo("Turn on backups (7 daily copies, +20%)?", p.Backups)

	with := "with IPv4"
	if p.Backups {
		with = "with IPv4 and backups"
	}
	var fits []buy.Candidate
	for {
		ans, err = a.text(fmt.Sprintf("Most per server, %s/mo before VAT %s:", cur, with), strconv.FormatFloat(p.MaxPrice, 'f', -1, 64), func(s string) error {
			if _, ok := parseMoney(s); !ok {
				return errors.New("a number above 0, please")
			}
			return nil
		})
		if err != nil {
			return p, err
		}
		p.MaxPrice, _ = parseMoney(ans)
		fits = (buy.Wants{Locations: p.Locations, MaxPrice: p.MaxPrice, Backups: p.Backups, MinMemory: minMemory}).Candidates(types, prices.IPv4)
		if len(fits) > 0 {
			break
		}
		open := (buy.Wants{Locations: p.Locations, Backups: p.Backups, MinMemory: minMemory}).Candidates(types, prices.IPv4)
		if len(open) == 0 {
			return p, fail("usage", "Hetzner sells nothing that fits in "+strings.Join(p.Locations, ", "), "Run setup again with other locations.")
		}
		cheapest := slices.MinFunc(open, func(x, y buy.Candidate) int { return cmp.Compare(x.Price, y.Price) })
		u.warn("Nothing costs that little; the cheapest is %s at %s", cheapest.Type, buy.Money(cheapest.Price, cur))
	}
	u.line("")
	slices.SortStableFunc(fits, func(x, y buy.Candidate) int { return cmp.Compare(x.Price, y.Price) })
	typeTable(u, fits, buy.Availability(types), cur)

	known := map[string]bool{}
	for _, t := range types {
		known[t.Name] = true
	}
	def := strings.Join(p.Types, ",")
	if def == "" {
		def = "any"
	}
	ans, err = a.text("Types, in order (any = all of these, cheapest first; cx23:6 gives one its own cap):", def, func(s string) error {
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

	def = "none"
	if p.MaxTotal > 0 {
		def = strconv.FormatFloat(p.MaxTotal, 'f', -1, 64)
	}
	ans, err = a.text(fmt.Sprintf("Most for all closedvps servers together, %s/mo (none = no limit):", cur), def, func(s string) error {
		if v, ok := parseMoney(s); strings.EqualFold(s, "none") || (ok && v >= p.MaxPrice) {
			return nil
		}
		return fmt.Errorf("a number of at least %s (one server), or none", strconv.FormatFloat(p.MaxPrice, 'f', -1, 64))
	})
	if err != nil {
		return p, err
	}
	p.MaxTotal, _ = parseMoney(ans) // "none" reads as 0, no limit

	if p.User, err = a.text("Linux user on the servers:", p.User, func(s string) error {
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
	if err := a.setupTailscale(ctx); err != nil {
		return p, err
	}

	if _, err := savePrefs(p); err != nil {
		return p, err
	}
	u.ok("Saved. closedvps create uses these from now on; flags change one run.")
	return p, nil
}

// setupTailscale offers to save a Tailscale OAuth client, checks it against the policy, and saves it only
// when it works and the policy has the tag.
func (a *app) setupTailscale(ctx context.Context) error {
	u := newUI(a.stderr, false, a.now)
	u.line("")
	if id, _, saved := tsCredentials(); saved {
		const keep, replace, remove = 0, 1, 2
		choice := keep
		if err := a.choose("Tailscale OAuth client "+id[:min(len(id), 4)]+"... is saved. Keep it?",
			[]string{"Keep it", "Replace it", "Remove it, and paste a key each time"}, &choice); err != nil {
			return err
		}
		switch choice {
		case keep:
			return nil
		case remove:
			if _, err := tsClientStore.forget(); err != nil {
				return err
			}
			u.ok("Removed the Tailscale OAuth client")
			return nil
		}
	} else if !a.yesNo("Save a Tailscale OAuth client? (recommended if you buy often)", false) {
		return nil
	}
	u.hint("With it, closedvps makes a one-off key per server and checks your policy before buying.")
	u.hint("If it leaked, it could add tagged devices and read your policy and device list,")
	u.hint("but not reach your devices or change the policy.")
	u.hint("1. Paste the blocks from closedvps.com/tailscale into Access controls")
	u.hint("2. console.tailscale.com/admin/settings/trust-credentials > Credential > OAuth")
	u.hint("   Scopes: Auth Keys (write, tag:closedvps), Policy File (read), Devices (read)")
	id, err := a.text("Client ID:", "", func(s string) error { return nil })
	if err != nil {
		return err
	}
	if id == "" {
		return fail("usage", "No client ID", "Run closedvps setup again to add one.")
	}
	secret, err := a.askSecret("Client secret:")
	if err != nil {
		return fail("usage", "No client secret: "+err.Error())
	}
	ts, err := tsapi.New(id, secret, a.tsBase, "closedvps/"+currentVersion())
	if err != nil {
		return err
	}
	policy, err := ts.CheckPolicy(ctx)
	switch {
	case err != nil && tsapi.Refused(err):
		return fail("bad_token", "Tailscale rejected that client",
			"Check it has the three scopes above, then run closedvps setup again. Nothing else changed.")
	case err != nil:
		return fail("failed", "Couldn't read your Tailscale policy: "+tsapi.Describe(err))
	case policy == tsapi.PolicyNoTag:
		return fail("no_tag", "Your Tailscale policy doesn't define tag:closedvps",
			"Add the blocks from closedvps.com/tailscale, then run closedvps setup again.")
	case policy == tsapi.PolicyOpen:
		u.warn("Your Tailscale policy lets tag:closedvps reach your devices")
		u.hint("closedvps.com/tailscale shows the rule that stops it.")
	default:
		u.ok("tag:closedvps exists and can't reach your devices")
	}
	if _, err := tsClientStore.save(id + "\n" + secret); err != nil {
		return err
	}
	return nil
}

// parseMoney reads an amount, with a comma or a dot for the decimals.
func parseMoney(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(s), ",", "."), 64)
	return v, err == nil && buy.ValidPrice(v)
}

// setupAlerts asks where remote alerts go, saves the links as a secret and sends a test message. Only
// "None" deletes saved links; a step that fails leaves them as they were.
func (a *app) setupAlerts(ctx context.Context) error {
	u := newUI(a.stderr, false, a.now)
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
	const title = "Send alerts to your phone or a chat? (a wait for stock can take days)"
	if err := a.question(huh.NewSelect[string]().Title(title).Options(opts...).Value(&choice), func() string {
		i := slices.IndexFunc(opts, func(o huh.Option[string]) bool { return o.Value == choice })
		return cyan.Render("?") + " " + title + " " + opts[i].Key
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
	if _, err := linksStore.save(link); err != nil {
		return err
	}
	if tip != "" {
		u.hint("%s", tip)
		if err := a.question(huh.NewInput().Title("Press Enter once you've subscribed, for a test message."), nil); err != nil {
			return err
		}
	}
	errs := alert.Send(ctx, a.http, parsed, alert.Message{Title: "closedvps test alert", Text: "Alerts from closedvps will arrive here."})
	for _, e := range errs {
		u.warn("The test alert failed for %v", e)
	}
	if len(errs) < len(parsed) {
		var names []string
		for _, l := range parsed {
			names = append(names, l.Name())
		}
		u.ok("Sent a test alert to %s", strings.Join(names, ", "))
		u.hint("If it didn't arrive, run closedvps setup again.")
	}
	return nil
}

func (a *app) connectTelegram(ctx context.Context) (string, error) {
	newUI(a.stderr, false, a.now).hint("In Telegram, message @BotFather with /newbot and copy the token it gives you.")
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
		Short: "Delete the saved Hetzner token, alert links and Tailscale OAuth client from this computer",
		Long: `Deletes the saved Hetzner token, alert links and Tailscale OAuth client from this computer. Revoke the
token in the Hetzner Console and the client in Tailscale's admin console too if you're done with them.`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			u := newUI(a.stdout, false, a.now)
			for _, s := range []store{tokenStore, linksStore, tsClientStore} {
				removed, err := s.forget()
				if err != nil {
					return err
				}
				if removed {
					u.ok("Deleted the saved %s", s.what)
				} else {
					u.info("No saved %s on this computer", s.what)
				}
			}
			return nil
		},
	}
}
