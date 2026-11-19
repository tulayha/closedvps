package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/spf13/cobra"

	"github.com/tulayha/closedvps/internal/alert"
	"github.com/tulayha/closedvps/internal/buy"
	"github.com/tulayha/closedvps/internal/cloud"
	"github.com/tulayha/closedvps/internal/cloudinit"
)

type createOpts struct {
	name, user, sshKey, image string
	types, locations, notify  string
	maxPrice, maxTotal        float64
	count                     int
	interval                  time.Duration
	backups                   bool
	claude, dryRun, yes       bool
	noWait, json              bool
	changed                   func(flag string) bool
}

// billingError is a run that bought at least one server but didn't finish: those servers are running and billing.
type billingError struct{ err error }

func (e billingError) Error() string { return e.err.Error() }
func (e billingError) Unwrap() error { return e.err }

const createLong = `Buys Hetzner Cloud servers behind a firewall with no inbound rules and sets them up on first boot:
Tailscale for SSH, automatic security updates, and optionally Cloudflare Tunnel and Claude Code. If nothing on
your list can be bought, it tries again until something can or you press Ctrl+C.

The first run in a terminal asks for your preferences (closedvps setup) and saves them; the options below
default to those answers and override them for one run.

Price limits: each server costs at most --max-price a month, or its own cap when the type carries one
(--type cx23:6,cx33:12). --max-total caps all closedvps servers in the Hetzner project together.

Secrets and settings from the environment:
  HCLOUD_TOKEN              Hetzner API token, Read & Write. Without it, the saved token, or it asks
                            and offers to save it in your system's password store.
  TS_AUTHKEY                Tailscale auth key, asked for when not set. Never saved. Reusable with --count.
  CLOUDFLARE_TUNNEL_TOKEN   optional: token of a Cloudflare Tunnel, for public web traffic
  CLOSEDVPS_NOTIFY          alert links, when --notify isn't given (else the ones saved by setup)
  CLOSEDVPS_SSH_KEY         the SSH public key itself, for machines without a key file (Docker)

Exit codes:
  0    every server bought and set up; when Tailscale runs on this computer (and without --no-wait),
       each one also answered SSH over your tailnet
  1    nothing bought
  3    something bought and billing, but not everything finished
  130  stopped with Ctrl+C before anything was bought`

func newCreateCmd(a *app) *cobra.Command {
	p, saved, perr := loadPrefs()
	var o createOpts
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Buy and set up servers (the first run asks what you want)",
		Long:  createLong,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if perr != nil {
				return perr
			}
			o.changed = cmd.Flags().Changed
			return a.create(cmd.Context(), o, p, saved)
		},
	}
	f := cmd.Flags()
	f.SortFlags = false
	f.StringVar(&o.name, "name", "vps", "server name, also its name in your tailnet")
	f.IntVar(&o.count, "count", 1, "how many servers to buy; above 1 they're named <name>-1, <name>-2, ...")
	f.StringVar(&o.types, "type", strings.Join(p.Types, ","), "server types to try, in order, each with an optional cap like cx23:6 (empty: every shared-vCPU type, cheapest first)")
	f.StringVar(&o.locations, "location", strings.Join(p.Locations, ","), "locations to try, in order")
	f.Float64Var(&o.maxPrice, "max-price", p.MaxPrice, "most you'll pay per server per month (server and IPv4, plus backups when on), before VAT, in your Hetzner account's currency")
	f.Float64Var(&o.maxTotal, "max-total", p.MaxTotal, "most all closedvps servers in this Hetzner project may cost together per month (0: no limit)")
	f.BoolVar(&o.backups, "backups", p.Backups, "turn on Hetzner Backups (7 daily copies, 20% of the server price)")
	f.StringVar(&o.user, "user", p.User, "the Linux user you log in as")
	f.StringVar(&o.sshKey, "ssh-key", p.SSHKey, "SSH public key file (default ~/.ssh/id_ed25519.pub, id_ecdsa.pub or id_rsa.pub)")
	f.StringVar(&o.notify, "notify", "", "alert links for this run, comma-separated (better in CLOSEDVPS_NOTIFY, which stays out of your shell history); none for local alerts only")
	f.BoolVar(&o.json, "json", false, "print one JSON event per line on stdout, for scripts and agents; the text goes to stderr")
	f.StringVar(&o.image, "image", "ubuntu-24.04", "Hetzner image")
	f.DurationVar(&o.interval, "every", time.Minute, "how long to wait between tries when nothing can be bought")
	f.BoolVar(&o.claude, "claude", false, "also install Claude Code (you log in on the server afterwards)")
	f.BoolVar(&o.dryRun, "dry-run", false, "show what would be bought and print the first-boot setup, without buying")
	f.BoolVar(&o.yes, "yes", false, "don't ask before buying")
	f.BoolVar(&o.noWait, "no-wait", false, "return once the servers are bought, without waiting for them to join your tailnet")
	return cmd
}

// createRun is one create, step by step: options, render, plan, prepare, buy, set up, wait until ready.
type createRun struct {
	*app
	o        createOpts
	rep      *reporter
	api      *cloud.Client // nil in a dry run without a token
	names    []string
	key      publicKey
	cfgs     []cloudinit.Config
	userData []string
	wants    buy.Wants
	currency string
	cands    []buy.Candidate
	spent    float64
	madeFW   map[string]*hcloud.Firewall // by server name: the firewalls this run created
	got      []buy.Purchase
}

func (a *app) create(ctx context.Context, o createOpts, p prefs, saved bool) (err error) {
	run := *a
	r := &createRun{app: &run, o: o}
	// Once anything is bought, every way out says so: an agent reading the exit code must know it's billing.
	defer func() {
		if err != nil && len(r.got) > 0 && !errors.As(err, new(billingError)) {
			err = billingError{err}
		}
	}()
	if err := r.options(ctx, p, saved); err != nil {
		return err
	}
	if err := r.render(); err != nil {
		return err
	}
	if err := r.plan(ctx); err != nil {
		return err
	}
	if r.o.dryRun {
		r.printSetup()
		return nil
	}
	if !r.o.yes && !r.confirm("Buy it?", false) {
		if !r.tty {
			return errors.New("not buying without confirmation; pass --yes")
		}
		return errors.New("nothing bought")
	}
	release := r.awake()
	defer release()

	b, err := r.prepare(ctx)
	if err != nil {
		return err
	}
	stopped, keepGoing := r.buy(ctx, b)
	if !keepGoing {
		return stopped
	}
	var problems []error
	if stopped != nil {
		problems = append(problems, stopped)
	}
	trouble, err := r.setUp(ctx)
	if err != nil {
		return err
	}
	unreachable, err := r.waitReady(ctx)
	if err != nil {
		return err
	}
	problems = append(problems, trouble...)
	if len(unreachable) > 0 {
		problems = append(problems, errors.New("not reachable yet: "+strings.Join(unreachable, ", ")))
	}
	if len(problems) > 0 {
		return billingError{errors.Join(problems...)}
	}
	return nil
}

// options checks the flags, fills in saved answers, asks first-run questions and finds the key and links.
func (r *createRun) options(ctx context.Context, p prefs, saved bool) error {
	o := &r.o
	if o.interval < 10*time.Second {
		return errors.New("--every must be at least 10s")
	}
	if o.count < 1 || o.count > 100 {
		return errors.New("--count must be between 1 and 100")
	}
	r.rep = &reporter{http: r.http, log: r.log, bell: r.stderr, desktop: r.desktop, now: r.now}
	if o.json {
		r.log = r.stderr
		r.rep.log = r.stderr
		r.rep.json = json.NewEncoder(r.stdout)
	}

	if o.dryRun && os.Getenv("HCLOUD_TOKEN") == "" && tokenStore.get() == "" {
		fmt.Fprintln(r.log, "# No Hetzner token, so this skips the stock check.")
	} else {
		api, err := r.connect()
		if err != nil {
			return err
		}
		r.api = api
	}
	if r.api != nil && !saved && r.tty {
		var err error
		if p, err = r.setupWizard(ctx, r.api, p); err != nil {
			return err
		}
		if !o.changed("user") {
			o.user = p.User
		}
		if !o.changed("type") {
			o.types = strings.Join(p.Types, ",")
		}
		if !o.changed("location") {
			o.locations = strings.Join(p.Locations, ",")
		}
		if !o.changed("max-price") {
			o.maxPrice = p.MaxPrice
		}
		if !o.changed("max-total") {
			o.maxTotal = p.MaxTotal
		}
		if !o.changed("backups") {
			o.backups = p.Backups
		}
	}
	if !buy.ValidPrice(o.maxPrice) {
		return errors.New("--max-price must be a number above 0")
	}
	if o.maxTotal != 0 && !buy.ValidPrice(o.maxTotal) {
		return errors.New("--max-total must be a number above 0, or 0 for no limit")
	}
	want, caps, err := buy.ParseTypes(splitList(o.types))
	if err != nil {
		return fmt.Errorf("--type: %w", err)
	}
	r.wants = buy.Wants{Types: want, Caps: caps, Locations: splitList(o.locations), MaxPrice: o.maxPrice, Backups: o.backups}

	linkText := o.notify
	if o.changed("notify") {
		if !strings.EqualFold(strings.TrimSpace(linkText), "none") {
			fmt.Fprintln(r.stderr, "Note: links given with --notify end up in your shell history; CLOSEDVPS_NOTIFY or `closedvps setup` keep them out.")
		}
	} else if linkText = os.Getenv("CLOSEDVPS_NOTIFY"); linkText == "" {
		linkText = linksStore.get()
	}
	if r.rep.links, err = alert.Parse(linkText); err != nil {
		return fmt.Errorf("alert links: %w", err)
	}

	r.names = []string{o.name}
	if o.count > 1 {
		r.names = nil
		for i := 1; i <= o.count; i++ {
			r.names = append(r.names, fmt.Sprintf("%s-%d", o.name, i))
		}
	}
	r.key, err = findPublicKey(o.sshKey, o.changed("ssh-key"))
	return err
}

// render makes each server's first-boot setup. Validation happens here, before anything is bought.
func (r *createRun) render() error {
	base := cloudinit.Config{Version: currentVersion(), User: r.o.user, SSHPublicKey: r.key.line, Claude: r.o.claude}
	if r.o.dryRun {
		base.TailscaleKey = "tskey-auth-dry-run"
		if os.Getenv("CLOUDFLARE_TUNNEL_TOKEN") != "" {
			base.TunnelToken = "dry-run"
		}
	} else {
		var err error
		if base.TailscaleKey, err = r.secret("TS_AUTHKEY", "Tailscale auth key:",
			"login.tailscale.com/admin/settings/keys > Generate auth key. "+keyAdvice(r.o.count)); err != nil {
			return err
		}
		base.TunnelToken = strings.TrimSpace(os.Getenv("CLOUDFLARE_TUNNEL_TOKEN"))
	}
	r.cfgs = make([]cloudinit.Config, len(r.names))
	r.userData = make([]string, len(r.names))
	for i, n := range r.names {
		r.cfgs[i] = base
		r.cfgs[i].Name = n
		r.cfgs[i].Ready = readyAlerts(r.rep.links, n, r.o.user)
		var err error
		if r.userData[i], err = cloudinit.Render(r.cfgs[i]); err != nil {
			return err
		}
	}
	return nil
}

func keyAdvice(count int) string {
	if count > 1 {
		return "Reusable and pre-approved, since several servers use it; let it expire within a day."
	}
	return "One-off and pre-approved is best."
}

// readyAlerts are the requests a server sends once it has joined the tailnet.
func readyAlerts(links []alert.Link, name, user string) []alert.Request {
	m := alert.Message{
		Title: name + " is ready", Text: "ssh " + user + "@" + name,
		Data: map[string]any{"v": 1, "event": "ready", "server": name, "ssh": user + "@" + name},
	}
	var reqs []alert.Request
	for _, l := range links {
		reqs = append(reqs, l.Requests(m)...)
	}
	return reqs
}

// plan checks the names and prices with Hetzner when there's a token, and prints what would be bought.
func (r *createRun) plan(ctx context.Context) error {
	if r.api != nil {
		types, err := r.api.ServerTypes(ctx)
		if err != nil {
			if cloud.IsCode(err, "unauthorized") {
				return errors.New("Hetzner didn't accept the token; make a new one in the Hetzner Console")
			}
			return fmt.Errorf("reading server types: %w", err)
		}
		prices, err := r.api.Prices(ctx)
		if err != nil {
			return fmt.Errorf("reading prices: %w", err)
		}
		r.currency = prices.Currency
		if err := checkNames(types, r.wants.Types, r.wants.Locations); err != nil {
			return err
		}
		if r.cands = r.wants.Candidates(types, prices.IPv4); len(r.cands) == 0 {
			return fmt.Errorf("nothing on your list costs %s or less; see `closedvps types` and raise --max-price", buy.Money(r.o.maxPrice, r.currency))
		}
		for _, n := range r.names {
			if existing, err := r.api.ServerByName(ctx, n); err != nil {
				return err
			} else if existing != nil {
				return fmt.Errorf("this project already has a server named %q; pick another with --name", n)
			}
		}
		if r.o.maxTotal > 0 {
			servers, err := r.api.Managed(ctx)
			if err != nil {
				return err
			}
			r.spent = buy.Spending(servers, prices.IPv4)
		}
	}

	fmt.Fprintf(r.log, "Servers  %s, log in as %s with %s\n", nameRange(r.names), r.o.user, r.key.from)
	fmt.Fprintf(r.log, "Firewall %s, no inbound rules\n", firewallLine(r.names))
	extras := []string{"Tailscale", "automatic security updates"}
	if r.cfgs[0].TunnelToken != "" {
		extras = append(extras, "Cloudflare Tunnel")
	}
	if r.o.claude {
		extras = append(extras, "Claude Code")
	}
	fmt.Fprintf(r.log, "Setup    %s on %s\n", strings.Join(extras, ", "), r.o.image)
	if r.o.backups {
		fmt.Fprintf(r.log, "Backups  on, 7 daily copies\n")
	}
	fmt.Fprintf(r.log, "Alerts   %s\n", alertLine(r.rep.links))
	if r.o.maxTotal > 0 {
		now := ""
		if r.api != nil {
			now = fmt.Sprintf(" (now %s)", buy.Money(r.spent, r.currency))
		}
		fmt.Fprintf(r.log, "Total    all closedvps servers in this project stay under %s%s\n", buy.Money(r.o.maxTotal, r.currency), now)
	}
	if r.o.count > 1 {
		fmt.Fprintln(r.log, "Tailscale: the auth key must be reusable, or only the first server joins your tailnet.")
	}
	if len(r.cands) > 0 {
		incl := "IPv4"
		if r.o.backups {
			incl = "IPv4 and backups"
		}
		what := "the first of these"
		if r.o.count > 1 {
			what = fmt.Sprintf("%d of the first of these", r.o.count)
		}
		fmt.Fprintf(r.log, "Buys %s Hetzner has in stock (per month with %s, before VAT):\n", what, incl)
		for i, c := range r.cands {
			if i == 8 {
				fmt.Fprintf(r.log, "  ...and %d more\n", len(r.cands)-i)
				break
			}
			fmt.Fprintf(r.log, "  %-6s %-5s %2d cores %4.0f GB %-4s %s\n", c.Type, c.Location, c.Cores, c.Memory, c.Arch, buy.Money(c.Price, r.currency))
		}
	}
	r.rep.emit(event{Event: "plan", Count: num(len(r.names)), Currency: r.currency, Candidates: r.cands})
	return nil
}

func (r *createRun) printSetup() {
	fmt.Fprintf(r.log, "\n# First-boot setup (cloud-init) it would send to %s, secrets hidden:\n", r.names[0])
	if len(r.names) > 1 {
		fmt.Fprintln(r.log, "# The others differ only in their name.")
	}
	fmt.Fprint(r.log, cloudinit.Redact(r.userData[0], r.cfgs[0]))
}

// prepare adds the SSH key and makes a closed firewall for each server, before any server exists, so none
// ever boots without one.
func (r *createRun) prepare(ctx context.Context) (*buy.Buyer, error) {
	host, _ := os.Hostname()
	keyName := fmt.Sprintf("closedvps %s %s", strings.ToLower(host), r.key.fingerprint[len(r.key.fingerprint)-5:])
	key, err := r.api.EnsureSSHKey(ctx, r.key.fingerprint, r.key.line, keyName)
	if err != nil {
		return nil, fmt.Errorf("adding your SSH key to Hetzner: %w", err)
	}
	b := &buy.Buyer{
		API: r.api, Wants: r.wants, MaxTotal: r.o.maxTotal, Interval: r.o.interval, Log: r.log,
		Currency: r.currency, Sleep: r.sleep, Now: r.now,
		Bought: func(p buy.Purchase, n int) { r.announce(p, n) },
		Waiting: func(check, bought int) {
			r.rep.emit(event{Event: "waiting", Check: check, Bought: num(bought), Count: num(len(r.names))})
		},
	}
	r.madeFW = map[string]*hcloud.Firewall{}
	for i, n := range r.names {
		fw, created, err := r.api.EnsureFirewall(ctx, cloud.FirewallName(n))
		if err != nil {
			r.removeFirewalls(nil)
			return nil, err
		}
		if created {
			r.madeFW[n] = fw
		}
		b.Slots = append(b.Slots, hcloud.ServerCreateOpts{
			Name: n, Image: &hcloud.Image{Name: r.o.image}, SSHKeys: []*hcloud.SSHKey{key},
			UserData: r.userData[i], Labels: cloud.Labels(),
			Firewalls: []*hcloud.ServerCreateFirewall{{Firewall: *fw}},
		})
	}
	fmt.Fprintf(r.log, "Firewall %s ready, no inbound rules\n", firewallLine(r.names))
	return b, nil
}

// announce reports a bought server: the event, and alerts here and to the links.
func (r *createRun) announce(p buy.Purchase, n int) {
	c := p.Candidate
	ev := event{
		Event: "bought", Server: p.Server.Name, Type: c.Type, Location: c.Location, Price: c.Price,
		Currency: r.currency, IPv4: cloud.IPv4(p.Server), Bought: num(n), Count: num(len(r.names)),
	}
	r.rep.emit(ev)
	text := fmt.Sprintf("%s in %s, %s. Setting it up now.", c.Type, c.Location, buy.Money(c.Price, r.currency))
	if len(r.names) > 1 {
		text += fmt.Sprintf(" %d of %d bought.", n, len(r.names))
	}
	r.rep.alert(p.Server.Name+" bought", text, ev, true)
}

// buy runs the buyer. When it stops early it looks for servers bought after all, deletes the firewalls nobody
// needs, and reports the stop. keepGoing says whether the servers bought should still be set up.
func (r *createRun) buy(ctx context.Context, b *buy.Buyer) (stopped error, keepGoing bool) {
	got, runErr := b.Run(ctx)
	r.got = got
	var unknown []string
	if runErr != nil {
		found, unk := b.Recover(ctx)
		unknown = unk
		for _, p := range found {
			r.got = append(r.got, p)
			fmt.Fprintf(r.log, "%s was bought after all and is running (and billing) at %s.\n", p.Server.Name, cloud.IPv4(p.Server))
			r.announce(p, len(r.got))
		}
	}
	keep := map[string]bool{}
	for _, p := range r.got {
		keep[p.Server.Name] = true
	}
	for _, n := range unknown {
		keep[n] = true
	}
	r.removeFirewalls(keep)
	if runErr == nil {
		return nil, true
	}

	ev := event{Event: "stopped", Reason: runErr.Error(), Bought: num(len(r.got)), Count: num(len(r.names))}
	r.rep.emit(ev)
	if ctx.Err() == nil {
		msg := "Nothing was bought: " + runErr.Error()
		if len(r.got) > 0 {
			msg = fmt.Sprintf("%d of %d bought, then it stopped: %s", len(r.got), len(r.names), runErr)
		}
		r.rep.alert("closedvps stopped", msg, ev, true)
	}
	if len(unknown) > 0 {
		return billingError{fmt.Errorf("%w; couldn't check whether %s got bought after all, so they may be billing; run `closedvps list`",
			runErr, strings.Join(unknown, ", "))}, false
	}
	switch {
	case len(r.got) == 0:
		return runErr, false
	case ctx.Err() != nil:
		fmt.Fprintf(r.log, "\nStill running and billing: %s. `closedvps list` shows them.\n", nameList(r.got))
		return runErr, false
	}
	fmt.Fprintf(r.log, "\nStopped after buying %d of %d: %v\nSetting up the ones bought.\n", len(r.got), len(r.names), runErr)
	return runErr, true
}

// removeFirewalls deletes the firewalls this run made for servers it didn't buy.
func (r *createRun) removeFirewalls(keep map[string]bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for name, fw := range r.madeFW {
		if !keep[name] {
			_, _ = r.api.Firewall.Delete(ctx, fw)
		}
	}
}

// setUp waits for each server to start, turns on backups and checks its firewalls. A problem with one
// server doesn't stop the others; the problems come back as the first result.
func (r *createRun) setUp(ctx context.Context) ([]error, error) {
	var problems []error
	backupsOK := r.o.backups
	for _, p := range r.got {
		name := p.Server.Name
		if p.Action != nil {
			if err := r.api.Wait(ctx, p.Action); err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				problems = append(problems, fmt.Errorf("Hetzner is still starting %s (%w); check the Hetzner Console", name, err))
				fmt.Fprintf(r.log, "Hetzner is still starting %s: %v. Check the Hetzner Console.\n", name, err)
				continue
			}
		}
		if r.o.backups {
			if err := r.api.EnableBackups(ctx, p.Server); err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				backupsOK = false
				fmt.Fprintf(r.log, "Couldn't turn on backups for %s (%v); turn them on in the Hetzner Console under the server's Backups tab.\n", name, err)
			}
		}
		open, err := r.api.OpenFirewalls(ctx, p.Server.ID)
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			problems = append(problems, fmt.Errorf("couldn't check the firewalls on %s (%w); run `closedvps list`", name, err))
		case len(open) > 0:
			problems = append(problems, fmt.Errorf("other firewalls on %s let traffic in", name))
			fmt.Fprintf(r.log, "Warning: other firewalls on %s let traffic in: %s. Remove them in the Hetzner Console.\n", name, strings.Join(open, ", "))
		}
		fmt.Fprintf(r.log, "%s is running at %s, unreachable from the internet.\n", name, cloud.IPv4(p.Server))
	}
	if r.o.backups && backupsOK {
		fmt.Fprintln(r.log, "Backups are on.")
	}
	fmt.Fprintln(r.log, "First-boot setup takes 3 to 5 minutes.")
	return problems, nil
}

// waitReady waits for each server to answer SSH over the tailnet, when this computer runs Tailscale.
func (r *createRun) waitReady(ctx context.Context) ([]string, error) {
	bin := r.tailscale()
	if r.o.noWait || bin == "" {
		if !r.o.noWait {
			fmt.Fprintln(r.log, "Tailscale isn't installed on this computer, so I can't watch for the servers to join your tailnet.")
		}
		if len(r.rep.links) > 0 {
			fmt.Fprintln(r.log, "Each server sends a \"ready\" alert to your alert links once it's on your tailnet.")
		}
		for _, p := range r.got {
			fmt.Fprintf(r.log, "When %s shows up at login.tailscale.com/admin/machines: ssh %s@%s\n", p.Server.Name, r.o.user, p.Server.Name)
		}
		return nil, nil
	}
	var unreachable []string
	for _, p := range r.got {
		host, err := r.waitForTailnet(ctx, bin, p.Server.Name, r.o.user, p.Server.Created)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			unreachable = append(unreachable, p.Server.Name)
			ev := event{Event: "unreachable", Server: p.Server.Name, Reason: err.Error()}
			r.rep.emit(ev)
			r.rep.alert(p.Server.Name+" isn't reachable yet", err.Error(), ev, true)
			continue
		}
		ev := event{Event: "ready", Server: p.Server.Name, SSH: r.o.user + "@" + host}
		r.rep.emit(ev)
		// The server sends this one to the alert links itself.
		r.rep.alert(p.Server.Name+" is ready", "ssh "+r.o.user+"@"+host, ev, false)
	}
	return unreachable, nil
}

func nameRange(names []string) string {
	if len(names) == 1 {
		return names[0]
	}
	return fmt.Sprintf("%s to %s (%d)", names[0], names[len(names)-1], len(names))
}

func firewallLine(names []string) string {
	if len(names) == 1 {
		return cloud.FirewallName(names[0])
	}
	return cloud.FirewallName(names[0]) + " to " + cloud.FirewallName(names[len(names)-1])
}

func nameList(got []buy.Purchase) string {
	var n []string
	for _, p := range got {
		n = append(n, p.Server.Name)
	}
	return strings.Join(n, ", ")
}

func alertLine(links []alert.Link) string {
	if len(links) == 0 {
		return "on this computer only (closedvps setup or CLOSEDVPS_NOTIFY adds your phone or a chat)"
	}
	var n []string
	for _, l := range links {
		n = append(n, l.Name())
	}
	return "on this computer and to " + strings.Join(n, ", ")
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// checkNames catches typos in --type and --location, which would otherwise wait forever for nothing.
func checkNames(types []*hcloud.ServerType, want, locs []string) error {
	known, knownLoc := map[string]bool{}, map[string]bool{}
	for _, t := range types {
		known[t.Name] = true
		for _, l := range t.Locations {
			if l.Location != nil {
				knownLoc[l.Location.Name] = true
			}
		}
	}
	for _, w := range want {
		if !known[w] {
			return fmt.Errorf("Hetzner has no server type %q; see `closedvps types`", w)
		}
	}
	if len(locs) == 0 {
		return errors.New("--location is empty")
	}
	for _, l := range locs {
		if !knownLoc[l] {
			return fmt.Errorf("Hetzner has no location %q; see `closedvps types`", l)
		}
	}
	return nil
}
