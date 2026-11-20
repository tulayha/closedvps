package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/spf13/cobra"

	"github.com/tulayha/closedvps/internal/alert"
	"github.com/tulayha/closedvps/internal/buy"
	"github.com/tulayha/closedvps/internal/cloud"
	"github.com/tulayha/closedvps/internal/cloudinit"
	"github.com/tulayha/closedvps/internal/tsapi"
)

type createOpts struct {
	name, user, sshKey, image string
	types, locations, notify  string
	maxPrice, maxTotal        float64
	count                     int
	interval                  time.Duration
	backups                   bool
	claude, codex             bool
	dryRun, showSetup, yes    bool
	noWait, json              bool
	changed                   func(flag string) bool
}

// billingError is a run that bought at least one server but didn't finish: those servers are running and billing.
type billingError struct{ err error }

func (e billingError) Error() string { return e.err.Error() }
func (e billingError) Unwrap() error { return e.err }

const createLong = `Buys Hetzner Cloud servers behind a firewall with no inbound rules and sets them up on first boot:
Tailscale for SSH, automatic security updates, and optionally Cloudflare Tunnel, Claude Code and Codex. If nothing on
your list can be bought, it tries again until something can or you press Ctrl+C.

The first run asks only what the server is for and uses defaults for the rest; closedvps setup saves your own.
The options below default to the saved answers and override them for one run.

Price limits: each server costs at most --max-price a month, or its own cap when the type carries one
(--type cx23:6,cx33:12). --max-total caps all closedvps servers in the Hetzner project together.

Secrets and settings from the environment:
  HCLOUD_TOKEN              Hetzner API token, Read & Write. Without it, the saved token, or it asks
                            and offers to save it in your system's password store.
  TS_AUTHKEY                Tailscale auth key. Asked for after you say yes, when neither this nor an OAuth
                            client is set. Never saved. Reusable with --count.
  TS_OAUTH_CLIENT_ID        Tailscale OAuth client (with TS_OAUTH_CLIENT_SECRET), instead of a key: closedvps
  TS_OAUTH_CLIENT_SECRET    makes a one-off key per server and checks your policy first. closedvps setup saves one.
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
		Short: "Buy and set up servers",
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
	f.BoolVar(&o.claude, "claude", p.Claude, "also install Claude Code (you sign in on the server afterwards); only types with 4 GB or more")
	f.BoolVar(&o.codex, "codex", p.Codex, "also install OpenAI's Codex CLI (you sign in on the server afterwards); only types with 4 GB or more")
	f.BoolVarP(&o.dryRun, "dry-run", "n", false, "show what would be bought, without buying")
	f.BoolVar(&o.showSetup, "show-setup", false, "with --dry-run, also print the first-boot setup (cloud-init) it would send")
	f.BoolVar(&o.yes, "yes", false, "don't ask before buying")
	f.BoolVar(&o.noWait, "no-wait", false, "return once the servers are bought, without waiting for them to join your tailnet")
	return cmd
}

// createRun is one create, step by step.
type createRun struct {
	*app
	o     createOpts
	p     prefs
	saved bool
	ui    *ui
	rep   *reporter

	api      *cloud.Client // nil in a dry run without a token
	ts       *tsapi.Client // nil when the key is pasted or comes from TS_AUTHKEY
	tsKey    string
	keyEnv   bool // tsKey came from TS_AUTHKEY
	policy   tsapi.Policy
	names    []string
	key      publicKey
	wants    buy.Wants
	currency string
	types    []*hcloud.ServerType
	ipv4     map[string]float64
	cands    []buy.Candidate
	skipped  []string // types on the list too small for an agent
	spent    float64
	madeFW   map[string]*hcloud.Firewall
	got      []buy.Purchase
	keys     map[string]mintedKey

	confirmed   bool
	stopped     bool // a stopped event was written
	purposeSet  string
	soldOut     bool // the sold-out lines were printed
	refusedOnce bool
	since       time.Time
	checks      int
	nextAt      time.Time
}

type mintedKey struct {
	key  string
	made time.Time
}

func (a *app) create(ctx context.Context, o createOpts, p prefs, saved bool) (err error) {
	run := *a
	r := &createRun{app: &run, o: o, p: p, saved: saved}
	defer func() { err = r.finish(err) }()
	for _, step := range []func(context.Context) error{
		r.options, r.sshKey, r.connect, r.purpose, r.tailscale, r.plan,
	} {
		if err := step(ctx); err != nil {
			return err
		}
	}
	if r.o.dryRun {
		r.ui.info("Nothing bought (dry run)")
		if r.o.showSetup {
			return r.printSetup()
		}
		return nil
	}
	if err := r.confirm(); err != nil {
		return err
	}
	if err := r.pasteKey(); err != nil {
		return err
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
	problems = append(problems, trouble...)
	notReady, err := r.waitReady(ctx)
	if err != nil {
		return err
	}
	problems = append(problems, notReady...)
	if len(problems) > 0 {
		return billingError{errors.Join(problems...)}
	}
	return nil
}

// finish prints a failure the run hasn't printed yet, writes the stopped event agents wait for, and keeps
// the exit code honest: once anything is bought, every way out says it's billing.
func (r *createRun) finish(err error) error {
	if err == nil {
		return nil
	}
	var f *failure
	if !errors.As(err, &f) {
		code := "failed"
		switch {
		case errors.Is(err, context.Canceled):
			code = "interrupted"
		case errors.Is(err, buy.ErrOverTotal):
			code = "over_cap"
		case len(r.got) > 0:
			code = "partial"
		}
		f = &failure{code: code, msg: err.Error(), err: err, shown: errors.As(err, new(billingError))}
	}
	if f.code == "interrupted" && len(r.got) == 0 {
		f.msg, f.neutral, f.hints = "Nothing was bought", true, nil
	} else if r.confirmed && len(r.got) == 0 && !f.neutral && !strings.HasPrefix(strings.Join(f.hints, " "), "Nothing was bought") {
		if len(f.hints) == 0 {
			f.hints = []string{"Nothing was bought."}
		} else {
			f.hints = append([]string{"Nothing was bought. " + f.hints[0]}, f.hints[1:]...)
		}
	}
	if r.ui != nil && !f.shown {
		r.ui.done()
		r.ui.report(f)
	}
	f.shown = true
	if r.rep != nil && !r.stopped {
		ev := event{Event: "stopped", Code: f.code, Reason: f.Error(), Bought: num(len(r.got)), Count: num(max(len(r.names), 1))}
		for _, p := range r.got {
			ev.Billing = append(ev.Billing, p.Server.Name)
		}
		r.rep.emit(ev)
		r.stopped = true
	}
	if len(r.got) > 0 {
		return billingError{f}
	}
	return f
}

// options checks the flags and finds the alert links.
func (r *createRun) options(context.Context) error {
	o := &r.o
	// Everything here is a message for a person, so it goes to stderr; the events and the --show-setup
	// cloud-init are the data, on stdout.
	r.rep = &reporter{http: r.http, log: r.stderr, bell: r.stderr, desktop: r.desktop, now: r.now}
	if o.json {
		r.rep.json = json.NewEncoder(r.stdout)
	}
	r.ui = newUI(r.stderr, r.watched, r.now)
	if o.interval < 10*time.Second {
		return fail("usage", "--every must be at least 10s")
	}
	if o.count < 1 || o.count > 100 {
		return fail("usage", "--count must be between 1 and 100")
	}
	if o.showSetup && !o.dryRun {
		return fail("usage", "--show-setup goes with --dry-run")
	}
	linkText := o.notify
	if o.changed("notify") {
		if !strings.EqualFold(strings.TrimSpace(linkText), "none") {
			r.ui.warn("Links given with --notify end up in your shell history")
			r.ui.hint("CLOSEDVPS_NOTIFY or closedvps setup keep them out.")
		}
	} else if linkText = os.Getenv("CLOSEDVPS_NOTIFY"); linkText == "" {
		linkText = linksStore.get()
	}
	var err error
	if r.rep.links, err = alert.Parse(linkText); err != nil {
		return fail("usage", "Alert links: "+err.Error())
	}
	return nil
}

// sshKey finds the key to log in with, and offers to make one in a terminal that has none.
func (r *createRun) sshKey(context.Context) error {
	var err error
	r.key, err = findPublicKey(r.o.sshKey, r.o.changed("ssh-key"))
	if err == nil || !errors.Is(err, errNoKey) {
		return err
	}
	if !r.tty {
		return fail("needs_key", "No SSH key in ~/.ssh", "Make one with ssh-keygen -t ed25519, or set CLOSEDVPS_SSH_KEY or --ssh-key.")
	}
	r.ui.warn("No SSH key in ~/.ssh")
	if !r.yesNo("Make one with ssh-keygen now?", true) {
		return fail("needs_key", "No SSH key to log in with", "Make one with ssh-keygen -t ed25519, or pass --ssh-key.")
	}
	if err := r.keygen(); err != nil {
		return fail("needs_key", "ssh-keygen didn't finish: "+err.Error())
	}
	if r.key, err = findPublicKey("", false); err != nil {
		return err
	}
	r.ui.ok("Using %s", shortPath(r.key.from))
	return nil
}

func (r *createRun) connect(context.Context) error {
	if r.o.dryRun && os.Getenv("HCLOUD_TOKEN") == "" && tokenStore.get() == "" {
		r.ui.warn("No Hetzner token, so this skips the stock check")
		return nil
	}
	api, err := r.app.connect()
	if err != nil {
		return err
	}
	r.api = api
	return nil
}

type purpose struct {
	label         string
	claude, codex bool
}

var purposes = []purpose{
	{"Just a server", false, false},
	{"Claude Code", true, false},
	{"Codex", false, true},
	{"Claude Code and Codex", true, true},
}

func purposeName(claude, codex bool) string {
	for _, p := range purposes {
		if p.claude == claude && p.codex == codex {
			return p.label
		}
	}
	return ""
}

// purpose settles what's installed: the flags, else the saved answer, else the one first-run question.
func (r *createRun) purpose(context.Context) error {
	o := &r.o
	switch {
	case o.changed("claude") || o.changed("codex"):
		if r.saved && (o.claude != r.p.Claude || o.codex != r.p.Codex) {
			r.purposeSet = fmt.Sprintf(" (this run; saved: %s)", purposeName(r.p.Claude, r.p.Codex))
		}
	case !r.saved && r.tty:
		choice := 0
		if err := r.choose("What's the server for?", purposeLabels(), &choice); err != nil {
			return err
		}
		o.claude, o.codex = purposes[choice].claude, purposes[choice].codex
	}
	if o.claude || o.codex {
		r.wants.MinMemory = agentMemory
	}
	return nil
}

func purposeLabels() []string {
	var l []string
	for _, p := range purposes {
		l = append(l, p.label)
	}
	return l
}

// tailscale settles where the key comes from, and with an OAuth client, checks the policy before anything is bought.
func (r *createRun) tailscale(ctx context.Context) error {
	if k := strings.TrimSpace(os.Getenv("TS_AUTHKEY")); k != "" {
		r.tsKey, r.keyEnv = k, true
		return nil
	}
	id, secret, savedClient := tsCredentials()
	if id == "" {
		return nil
	}
	ts, err := tsapi.New(id, secret, r.tsBase, "closedvps/"+currentVersion())
	if err != nil {
		return err
	}
	r.ts = ts
	r.policy, err = ts.CheckPolicy(ctx)
	client, defines := "the OAuth client in TS_OAUTH_CLIENT_ID", "doesn't define"
	fix := "Replace it, or set TS_AUTHKEY for this run."
	if savedClient {
		client, defines = "the saved OAuth client", "no longer defines"
		fix = "Run closedvps setup to replace it, or set TS_AUTHKEY for this run."
	}
	switch {
	case err != nil && tsapi.Refused(err):
		return fail("bad_token", "Tailscale rejected "+client, "Nothing was bought. "+fix)
	case err != nil:
		return fail("failed", "Couldn't read your Tailscale policy: "+tsapi.Describe(err), "Nothing was bought.")
	case r.policy == tsapi.PolicyNoTag:
		return fail("no_tag", "Your Tailscale policy "+defines+" tag:closedvps",
			"Nothing was bought. Add the blocks from closedvps.com/tailscale and run this again.")
	}
	return nil
}

func (r *createRun) memory(t string) float64 {
	for _, x := range r.types {
		if x.Name == t {
			return float64(x.Memory)
		}
	}
	return 0
}

// agentMemory is the least Claude Code and Codex ask for, in GB (code.claude.com/docs/en/setup and
// github.com/openai/codex/blob/main/docs/install.md).
const agentMemory = 4

func (r *createRun) agents() string {
	switch {
	case r.o.claude && r.o.codex:
		return "Claude Code and Codex"
	case r.o.codex:
		return "Codex"
	}
	return "Claude Code"
}

// plan checks names, prices and caps with Hetzner, and shows what would be bought.
func (r *createRun) plan(ctx context.Context) error {
	o := &r.o
	if !buy.ValidPrice(o.maxPrice) {
		return fail("usage", "--max-price must be a number above 0")
	}
	if o.maxTotal != 0 && !buy.ValidPrice(o.maxTotal) {
		return fail("usage", "--max-total must be a number above 0, or 0 for no limit")
	}
	want, caps, err := buy.ParseTypes(splitList(o.types))
	if err != nil {
		return fail("usage", "--type: "+err.Error())
	}
	r.wants.Types, r.wants.Caps, r.wants.Locations = want, caps, splitList(o.locations)
	r.wants.MaxPrice, r.wants.Backups = o.maxPrice, o.backups
	r.names = []string{o.name}
	if o.count > 1 {
		r.names = nil
		for i := 1; i <= o.count; i++ {
			r.names = append(r.names, fmt.Sprintf("%s-%d", o.name, i))
		}
	}
	if r.api != nil {
		if err := r.checkStock(ctx); err != nil {
			return err
		}
	}
	if err := r.render(); err != nil {
		return err
	}
	r.printPlan(ctx)
	ev := event{
		Event: "plan", DryRun: o.dryRun, Servers: r.names, Setup: r.setupList(), Tailscale: r.tsMode(),
		Count: num(len(r.names)), Currency: r.currency,
	}
	avail := buy.Availability(r.types)
	for _, c := range r.cands {
		ev.Candidates = append(ev.Candidates, planCandidate{c, avail[c.String()]})
	}
	r.rep.emit(ev)
	return nil
}

func (r *createRun) checkStock(ctx context.Context) error {
	o := &r.o
	types, err := r.api.ServerTypes(ctx)
	if err != nil {
		if cloud.IsCode(err, "unauthorized") {
			return fail("bad_token", "Hetzner didn't accept the token",
				"Make a new one at console.hetzner.com > Security > API tokens, Read & Write.")
		}
		return fmt.Errorf("reading server types: %w", err)
	}
	prices, err := r.api.Prices(ctx)
	if err != nil {
		return fmt.Errorf("reading prices: %w", err)
	}
	r.types, r.ipv4, r.currency = types, prices.IPv4, prices.Currency
	if err := checkNames(types, r.wants.Types, r.wants.Locations); err != nil {
		return err
	}
	if r.wants.MinMemory > 0 && len(r.wants.Types) > 0 {
		var small []string
		for _, t := range types {
			if slices.Contains(r.wants.Types, t.Name) && float64(t.Memory) < r.wants.MinMemory {
				small = append(small, fmt.Sprintf("%s has %s", t.Name, gb(float64(t.Memory))))
				r.skipped = append(r.skipped, t.Name)
			}
		}
		if len(r.skipped) == len(r.wants.Types) {
			fit := buy.Wants{Locations: r.wants.Locations, MaxPrice: o.maxPrice, Backups: o.backups, MinMemory: agentMemory}
			var hints []string
			if c := firstOfEachType(fit.Candidates(types, r.ipv4), 3); len(c) > 0 {
				hints = append(hints, "Under your "+r.capText(o.maxPrice)+" cap: "+priceList(c),
					"Try: closedvps create "+r.agentFlags()+" --type "+c[0].Type)
			}
			return fail("too_small", fmt.Sprintf("%s and %s needs %d GB", strings.Join(small, ", "), r.agents(), agentMemory), hints...)
		}
	}
	if r.cands = r.wants.Candidates(types, r.ipv4); len(r.cands) == 0 {
		return r.overCap()
	}
	if err := r.pickNames(ctx); err != nil {
		return err
	}
	if o.maxTotal > 0 {
		servers, err := r.api.Managed(ctx)
		if err != nil {
			return err
		}
		r.spent = buy.Spending(servers, r.ipv4)
		cheapest := slices.MinFunc(r.cands, func(a, b buy.Candidate) int { return cmpFloat(a.Price, b.Price) })
		if r.spent+cheapest.Price > o.maxTotal+1e-9 {
			return fail("over_cap", fmt.Sprintf("Your closedvps servers already cost %.2f of your %s limit", r.spent, r.capText(o.maxTotal)),
				fmt.Sprintf("The cheapest type on your list is %.2f. See them with closedvps list.", cheapest.Price))
		}
	}
	return nil
}

// overCap says why nothing on the list fits the cap, and the cap that would.
func (r *createRun) overCap() error {
	o := &r.o
	open := r.wants
	open.MaxPrice, open.Caps = 0, nil
	c := open.Candidates(r.types, r.ipv4)
	what := "on your list"
	if r.wants.MinMemory > 0 {
		what = fmt.Sprintf("with %d GB", agentMemory)
	}
	if len(c) == 0 {
		return fail("over_cap", "Nothing "+what+" is sold in "+strings.Join(r.wants.Locations, ", "), "See closedvps types for what's sold where.")
	}
	cheapest := slices.MinFunc(c, func(a, b buy.Candidate) int { return cmpFloat(a.Price, b.Price) })
	try := strings.TrimSpace("closedvps create " + r.agentFlags())
	return fail("over_cap", fmt.Sprintf("Nothing %s costs %s or less", what, r.capText(o.maxPrice)),
		fmt.Sprintf("The cheapest is %s at %s.", cheapest.Type, r.money(cheapest.Price)),
		fmt.Sprintf("Try: %s --max-price %g", try, math.Ceil(cheapest.Price)))
}

func (r *createRun) agentFlags() string {
	var f []string
	if r.o.claude {
		f = append(f, "--claude")
	}
	if r.o.codex {
		f = append(f, "--codex")
	}
	return strings.Join(f, " ")
}

// pickNames makes sure no name is taken. In a terminal, a taken default name moves on to the next free one;
// anywhere else it stops, so a rerun after a restart can't buy a second server.
func (r *createRun) pickNames(ctx context.Context) error {
	taken := func(n string) (bool, error) {
		s, err := r.api.ServerByName(ctx, n)
		return s != nil, err
	}
	auto := r.tty && !r.o.json && !r.o.changed("name") && r.o.count == 1
	for i, n := range r.names {
		t, err := taken(n)
		if err != nil {
			return err
		}
		if !t {
			continue
		}
		if !auto {
			return fail("name_taken", n+" already exists", "Nothing was bought. To buy another, pass a different --name.")
		}
		for k := 2; ; k++ {
			alt := fmt.Sprintf("%s-%d", n, k)
			if t, err := taken(alt); err != nil {
				return err
			} else if !t {
				r.ui.info("%s is taken, so this one is %s", n, alt)
				r.names[i] = alt
				break
			}
		}
	}
	return nil
}

// render makes each server's first-boot setup with a stand-in key, which checks it before anything is
// bought; the real key goes in when each server is bought.
func (r *createRun) render() error {
	_, err := r.userData(r.names[0], "tskey-auth-placeholder")
	return err
}

func (r *createRun) userData(name, key string) (string, error) {
	c := cloudinit.Config{
		Version: currentVersion(), Name: name, User: r.o.user, SSHPublicKey: r.key.line,
		TailscaleKey: key, Claude: r.o.claude, Codex: r.o.codex, Ready: readyAlerts(r.rep.links, name, r.o.user),
		TunnelToken: strings.TrimSpace(os.Getenv("CLOUDFLARE_TUNNEL_TOKEN")),
	}
	s, err := cloudinit.Render(c)
	if err != nil {
		return "", fail("usage", err.Error())
	}
	return s, nil
}

func (r *createRun) printSetup() error {
	ud, err := r.userData(r.names[0], "tskey-auth-hidden")
	if err != nil {
		return err
	}
	c := cloudinit.Config{
		Ready: readyAlerts(r.rep.links, r.names[0], r.o.user), TailscaleKey: "tskey-auth-hidden",
		TunnelToken: strings.TrimSpace(os.Getenv("CLOUDFLARE_TUNNEL_TOKEN")),
	}
	fmt.Fprintf(r.stdout, "# First-boot setup (cloud-init) for %s, secrets hidden.\n", r.names[0])
	if len(r.names) > 1 {
		fmt.Fprintln(r.stdout, "# The others differ only in their name.")
	}
	fmt.Fprint(r.stdout, cloudinit.Redact(ud, c))
	return nil
}

func (r *createRun) setupList() []string {
	s := []string{"tailscale", "updates"}
	if os.Getenv("CLOUDFLARE_TUNNEL_TOKEN") != "" {
		s = append(s, "tunnel")
	}
	if r.o.claude {
		s = append(s, "claude")
	}
	if r.o.codex {
		s = append(s, "codex")
	}
	return s
}

func (r *createRun) tsMode() string {
	switch {
	case r.ts == nil:
		return "key"
	case r.policy == tsapi.PolicyOpen:
		return "open"
	}
	return "checked"
}

func (r *createRun) money(v float64) string { return buy.Money(v, r.currency) }

// capText is a cap the way it was typed: 10 EUR/mo, 6.5 EUR/mo.
func (r *createRun) capText(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64) + " " + currencyOr(r.currency) + "/mo"
}

func gb(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) + " GB" }

func imageName(image string) string {
	if v, ok := strings.CutPrefix(image, "ubuntu-"); ok {
		return "Ubuntu " + v
	}
	if v, ok := strings.CutPrefix(image, "debian-"); ok {
		return "Debian " + v
	}
	return image
}

// printPlan shows the server, then one line per type with the locations it's in stock in.
func (r *createRun) printPlan(ctx context.Context) {
	o := &r.o
	u := r.ui
	for _, t := range r.skipped {
		u.warn("Skipping %s: %s, and %s needs %d GB", t, gb(r.memory(t)), r.agents(), agentMemory)
	}
	who := "ssh " + o.user + "@" + r.names[0]
	label, name, fw := "Server", r.names[0], cloud.FirewallName(r.names[0])
	if len(r.names) > 1 {
		label, who = "Servers", "ssh "+o.user+"@<name>"
		name = fmt.Sprintf("%s to %s", r.names[0], r.names[len(r.names)-1])
		fw = cloud.FirewallName(r.names[0]) + " to " + cloud.FirewallName(r.names[len(r.names)-1])
	}
	u.line("")
	row := func(k, v string) { u.line("  %-10s %s", k, v) }
	row(label, fmt.Sprintf("%s · %s · %s over Tailscale", name, imageName(o.image), who))
	var inst []string
	if o.claude {
		inst = append(inst, "Claude Code")
	}
	if o.codex {
		inst = append(inst, "Codex")
	}
	if os.Getenv("CLOUDFLARE_TUNNEL_TOKEN") != "" {
		inst = append(inst, "Cloudflare Tunnel")
	}
	if len(inst) > 0 {
		row("Installs", strings.Join(inst, ", ")+r.purposeSet)
	}
	row("Firewall", fw+", no inbound rules")
	switch {
	case r.ts != nil:
		row("Tailscale", "one-off key, policy checked")
	case r.keyEnv:
		row("Tailscale", "key from TS_AUTHKEY")
	}
	if o.backups {
		row("Backups", "on, 7 daily copies")
	} else {
		row("Backups", "off")
	}
	if len(r.rep.links) > 0 {
		var n []string
		for _, l := range r.rep.links {
			n = append(n, l.Name())
		}
		row("Alerts", strings.Join(n, ", "))
	}
	if o.maxTotal > 0 {
		row("Total", fmt.Sprintf("all closedvps servers stay under %s (now %.2f)", r.money(o.maxTotal), r.spent))
	}
	if r.api != nil {
		r.printTypes()
	}
	u.line("")
	if r.ts != nil && r.policy == tsapi.PolicyOpen {
		u.warn("Your Tailscale policy lets tag:closedvps reach your devices")
		u.hint("closedvps.com/tailscale shows the rule that stops it.")
	}
	if r.tty && !o.json && !o.dryRun && !r.onTailnet(ctx) {
		u.warn("This computer isn't on a tailnet, so you can't ssh to %s from here", r.names[0])
		u.hint("Install Tailscale first (tailscale.com/download), or buy anyway and connect from another device.")
	}
	if !r.tty && !o.json && !o.dryRun {
		if len(r.rep.links) == 0 {
			u.warn("No alert links, so nothing will tell you when %s is bought", r.names[0])
			u.hint("Set CLOSEDVPS_NOTIFY, or save links with closedvps setup.")
		}
		if r.keyEnv {
			u.warn("A pasted Tailscale key stops working on the expiry date you set")
			u.hint("If the wait runs past it, the server is bought but can't join.")
			u.hint("A saved OAuth client (closedvps setup) makes the key when it buys instead.")
		}
	}
}

// printTypes is the plan's table: one line per type, in the order they're tried.
func (r *createRun) printTypes() {
	r.ui.line("")
	typeTable(r.ui, r.cands, buy.Availability(r.types), r.currency)
	foot := "Before VAT, IPv4 included."
	if r.o.backups {
		foot = "Before VAT, IPv4 and backups included."
	}
	if !r.saved && r.tty {
		foot += " Change the defaults with closedvps setup."
	}
	r.ui.line("  %s", faint.Render(foot))
	if r.wants.MinMemory > 0 && len(r.wants.Types) == 0 {
		r.ui.line("  %s", faint.Render(fmt.Sprintf("%s needs %d GB, so smaller types are left out.", r.agents(), agentMemory)))
	}
}

// typeTable prints one line per type in cands, in their order, with its price (the lowest, when it differs
// by location) and the locations it's in stock in. It shows 8 types at most.
func typeTable(u *ui, cands []buy.Candidate, avail map[string]bool, currency string) {
	type row struct {
		c         buy.Candidate
		low, high float64
		stock     []string
	}
	var rows []*row
	byType := map[string]*row{}
	for _, c := range cands {
		rw := byType[c.Type]
		if rw == nil {
			rw = &row{c: c, low: c.Price, high: c.Price}
			byType[c.Type] = rw
			rows = append(rows, rw)
		}
		rw.low, rw.high = min(rw.low, c.Price), max(rw.high, c.Price)
		if avail[c.String()] {
			rw.stock = append(rw.stock, c.Location)
		}
	}
	var b bytes.Buffer
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "TYPE\tCPU\tRAM\tARCH\t%s/MO\tIN STOCK\n", currencyOr(currency))
	for i, rw := range rows {
		if i == 8 {
			break
		}
		price := fmt.Sprintf("%.2f", rw.low)
		if rw.high > rw.low+1e-9 {
			price = "from " + price
		}
		stock := "-"
		if len(rw.stock) > 0 {
			stock = strings.Join(rw.stock, " ")
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\n", rw.c.Type, rw.c.Cores, gb(rw.c.Memory), rw.c.Arch, price, stock)
	}
	_ = tw.Flush()
	for line := range strings.Lines(b.String()) {
		u.line("  %s", strings.TrimRight(line, "\n"))
	}
	if len(rows) > 8 {
		u.line("  %s", faint.Render(fmt.Sprintf("and %d more; closedvps types lists them", len(rows)-8)))
	}
}

func (r *createRun) confirm() error {
	if r.o.yes {
		r.confirmed = true
		return nil
	}
	q := "Buy the first one Hetzner sells you?"
	if len(r.names) > 1 {
		q = fmt.Sprintf("Buy %d servers?", len(r.names))
	}
	if !r.tty {
		return fail("needs_yes", "Not buying without a yes", "Pass --yes to buy without being asked.")
	}
	if !r.yesNo(q, false) {
		return &failure{code: "declined", msg: "Nothing was bought", neutral: true}
	}
	r.confirmed = true
	return nil
}

// pasteKey asks for the Tailscale key once the plan is agreed, when it doesn't come from elsewhere.
func (r *createRun) pasteKey() error {
	if r.ts != nil || r.tsKey != "" {
		return nil
	}
	if !r.tty {
		return fail("needs_key", "TS_AUTHKEY is not set",
			"Make a one-off key at login.tailscale.com/admin/settings/keys and set TS_AUTHKEY,",
			"or save a Tailscale OAuth client with closedvps setup.")
	}
	q := "Tailscale auth key (login.tailscale.com/admin/settings/keys, one-off)"
	if len(r.names) > 1 {
		q = fmt.Sprintf("Tailscale auth key for all %d (Reusable, expiring within a day)", len(r.names))
	}
	k, err := r.askSecret(q)
	if err != nil {
		return fail("needs_key", "No Tailscale key: "+err.Error())
	}
	if !strings.HasPrefix(k, "tskey-") {
		return fail("needs_key", "That isn't a Tailscale auth key", "Auth keys start with tskey-auth-.")
	}
	r.tsKey = k
	return nil
}

// keyFor is the Tailscale key for a server's next try: the pasted one, or a one-off key made now and remade
// once it gets old, since a wait for stock can outlast it.
func (r *createRun) keyFor(ctx context.Context, name string) (string, error) {
	if r.ts == nil {
		return r.tsKey, nil
	}
	if r.keys == nil {
		r.keys = map[string]mintedKey{}
	}
	if k, ok := r.keys[name]; ok && r.now().Sub(k.made) < 50*time.Minute {
		return k.key, nil
	}
	k, err := r.ts.NewKey(ctx, name, time.Hour)
	if err != nil {
		return "", err
	}
	r.keys[name] = mintedKey{k, r.now()}
	return k, nil
}

// prepare adds the SSH key and makes a closed firewall for each server, before any server exists, so none
// ever boots without one.
func (r *createRun) prepare(ctx context.Context) (*buy.Buyer, error) {
	host, _ := os.Hostname()
	keyName := fmt.Sprintf("closedvps %s %s", strings.ToLower(host), r.key.fingerprint[len(r.key.fingerprint)-5:])
	key, err := r.api.EnsureSSHKey(ctx, r.key.fingerprint, r.key.line, keyName)
	if err != nil {
		if cloud.IsCode(err, "forbidden", "unauthorized") {
			return nil, fail("bad_token", "Hetzner refused to add your SSH key: the token is read-only",
				"Make a Read & Write token at console.hetzner.com > Security > API tokens.")
		}
		return nil, fmt.Errorf("adding your SSH key to Hetzner: %w", err)
	}
	b := &buy.Buyer{
		API: r.api, Wants: r.wants, MaxTotal: r.o.maxTotal, Interval: r.o.interval,
		Currency: r.currency, Sleep: r.sleep,
		Bought:  func(p buy.Purchase, n int) { r.announce(p, n) },
		Waiting: r.waiting,
		Refused: func(c buy.Candidate, msg string) {
			r.ui.warn("Hetzner lists %s in %s in stock but refused it: %s", c.Type, c.Location, msg)
			if !r.refusedOnce {
				r.refusedOnce = true
				r.ui.hint("New accounts get this for a while. Still trying.")
			}
		},
		Failed: func(what string, err error) {
			r.ui.warn("%s failed: %v", upperFirst(what), err)
			r.ui.hint("Trying again at the next check.")
		},
		UserData: func(name string) (string, error) {
			k, err := r.keyFor(ctx, name)
			if err != nil {
				return "", err
			}
			return r.userData(name, k)
		},
	}
	r.madeFW = map[string]*hcloud.Firewall{}
	for _, n := range r.names {
		fw, created, err := r.api.EnsureFirewall(ctx, cloud.FirewallName(n))
		if err != nil {
			r.removeFirewalls(nil)
			if cloud.IsCode(err, "forbidden", "unauthorized") {
				return nil, fail("bad_token", "Hetzner refused to create the firewall: the token is read-only",
					"Make a Read & Write token at console.hetzner.com > Security > API tokens.")
			}
			return nil, fmt.Errorf("making the firewall %s: %w", cloud.FirewallName(n), err)
		}
		if created {
			r.madeFW[n] = fw
		}
		b.Slots = append(b.Slots, hcloud.ServerCreateOpts{
			Name: n, Image: &hcloud.Image{Name: r.o.image}, SSHKeys: []*hcloud.SSHKey{key},
			Labels: cloud.Labels(), Firewalls: []*hcloud.ServerCreateFirewall{{Firewall: *fw}},
		})
	}
	if len(r.names) == 1 {
		r.ui.ok("Created firewall %s", cloud.FirewallName(r.names[0]))
	} else {
		r.ui.ok("Created firewalls %s to %s", cloud.FirewallName(r.names[0]), cloud.FirewallName(r.names[len(r.names)-1]))
	}
	r.since = r.now()
	return b, nil
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// waiting runs after each look that left something to buy: the first time it says what's missing, then it
// keeps the waiting line up to date.
func (r *createRun) waiting(l buy.Look) {
	r.checks = l.Check
	r.nextAt = r.now().Add(r.o.interval)
	r.rep.emit(event{Event: "waiting", Check: l.Check, Bought: num(l.Bought), Count: num(len(r.names))})
	if !r.soldOut {
		r.soldOut = true
		r.ui.done()
		r.explainSoldOut(l)
	}
	r.ui.wait(r.stockLine)
	what := "still waiting"
	if l.Bought > 0 {
		what = fmt.Sprintf("still waiting for %d more", len(r.names)-l.Bought)
	}
	r.ui.still(fmt.Sprintf("%s · %s", what, plural(l.Check, "1 check", fmt.Sprintf("%d checks", l.Check))))
}

func (r *createRun) stockLine() string {
	what := "stock"
	if n := len(r.names) - len(r.got); len(r.got) > 0 {
		what = fmt.Sprintf("%d more", n)
	}
	next := r.nextAt.Sub(r.now())
	return fmt.Sprintf("Waiting for %s · %s since %s · next in %s · Ctrl+C stops",
		what, plural(r.checks, "1 check", fmt.Sprintf("%d checks", r.checks)), r.since.Format("15:04"), since(max(next, 0)))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// explainSoldOut says what's in stock that the list rules out, so the wait isn't a mystery.
func (r *createRun) explainSoldOut(l buy.Look) {
	if l.Bought > 0 {
		r.ui.info("%d of %d bought, waiting for the rest", l.Bought, len(r.names))
		return
	}
	r.ui.warn("Nothing on your list is in stock")
	avail := buy.Availability(l.Types)
	inStock := func(cs []buy.Candidate) []buy.Candidate {
		var out []buy.Candidate
		for _, c := range cs {
			if avail[c.String()] {
				out = append(out, c)
			}
		}
		return out
	}
	if r.wants.MinMemory > 0 {
		small := r.wants
		small.MinMemory = 0
		var names []string
		for _, c := range inStock(small.Candidates(l.Types, l.IPv4)) {
			if c.Memory < r.wants.MinMemory && !slices.Contains(names, c.Type) {
				names = append(names, fmt.Sprintf("%s (%s)", c.Type, gb(c.Memory)))
			}
		}
		if len(names) > 0 {
			r.ui.hint("In stock but too small for %s: %s", r.agents(), strings.Join(names, ", "))
		}
	}
	open := r.wants
	open.MaxPrice, open.Caps = 0, nil
	if c := inStock(open.Candidates(l.Types, l.IPv4)); len(c) > 0 {
		cheapest := slices.MinFunc(c, func(a, b buy.Candidate) int { return cmpFloat(a.Price, b.Price) })
		r.ui.hint("In stock above your %s cap: %s (%s) at %s in %s", r.capText(r.o.maxPrice), cheapest.Type, gb(cheapest.Memory), r.money(cheapest.Price), cheapest.Location)
	}
}

// announce reports a bought server: the line, the event, and alerts here and to the links.
func (r *createRun) announce(p buy.Purchase, n int) {
	c := p.Candidate
	r.ui.done()
	r.soldOut = false
	line := fmt.Sprintf("Bought %s in %s for %s (%s, %s)", c.Type, c.Location, r.money(c.Price), p.Server.Name, cloud.IPv4(p.Server))
	if len(r.names) > 1 {
		line += fmt.Sprintf(" · %d of %d", n, len(r.names))
	}
	r.ui.ok("%s", line)
	ev := event{
		Event: "bought", Server: p.Server.Name, Type: c.Type, Location: c.Location, Price: c.Price,
		Currency: r.currency, IPv4: cloud.IPv4(p.Server), Bought: num(n), Count: num(len(r.names)),
	}
	r.rep.emit(ev)
	text := fmt.Sprintf("%s in %s, %s. Setting it up now.", c.Type, c.Location, r.money(c.Price))
	if len(r.names) > 1 {
		text += fmt.Sprintf(" %d of %d bought.", n, len(r.names))
	}
	r.rep.alert(p.Server.Name+" bought", text, ev, true)
}

// buy runs the buyer. When it stops early it looks for servers bought after all, deletes the firewalls nobody
// needs, and reports the stop. keepGoing says whether the servers bought should still be set up.
func (r *createRun) buy(ctx context.Context, b *buy.Buyer) (stopped error, keepGoing bool) {
	got, runErr := b.Run(ctx)
	r.ui.done()
	r.got = got
	var unknown []string
	if runErr != nil {
		found, unk := b.Recover(ctx)
		unknown = unk
		for _, p := range found {
			r.got = append(r.got, p)
			r.ui.warn("%s was bought after all and is running (and billing) at %s", p.Server.Name, cloud.IPv4(p.Server))
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
	removed := r.removeFirewalls(keep)
	if runErr == nil {
		return nil, true
	}
	if ctx.Err() != nil && len(removed) > 0 {
		r.ui.line("Stopping... deleted %s %s", plural(len(removed), "firewall", "firewalls"), strings.Join(removed, ", "))
	}

	f := r.stopFailure(runErr, unknown)
	r.ui.report(f)
	if ctx.Err() == nil {
		msg := "Nothing was bought: " + runErr.Error()
		if len(r.got) > 0 {
			msg = fmt.Sprintf("%d of %d bought, then it stopped: %s", len(r.got), len(r.names), runErr)
		}
		r.rep.alert("closedvps stopped", msg, event{Event: "stopped", Code: f.code, Reason: runErr.Error()}, true)
	}
	if len(unknown) > 0 || len(r.got) == 0 || ctx.Err() != nil {
		return f, false
	}
	r.ui.info("Setting up the %d bought", len(r.got))
	r.writeStopped(f)
	return billingError{f}, true
}

func (r *createRun) writeStopped(f *failure) {
	ev := event{Event: "stopped", Code: f.code, Reason: f.Error(), Bought: num(len(r.got)), Count: num(len(r.names))}
	for _, p := range r.got {
		ev.Billing = append(ev.Billing, p.Server.Name)
	}
	r.rep.emit(ev)
	r.stopped = true
}

func (r *createRun) stopFailure(runErr error, unknown []string) *failure {
	f := &failure{err: runErr, msg: runErr.Error(), code: "failed"}
	switch {
	case errors.Is(runErr, context.Canceled) && len(r.got) == 0:
		f.code, f.msg, f.neutral = "interrupted", "Nothing was bought", true
	case errors.Is(runErr, buy.ErrOverTotal):
		f.code = "over_cap"
	}
	if len(r.got) > 0 {
		f.code = "partial"
		var names []string
		for _, p := range r.got {
			names = append(names, p.Server.Name)
		}
		if ctx := errors.Is(runErr, context.Canceled); ctx {
			f.msg = "Stopped. Still running and billing: " + strings.Join(names, ", ")
		} else {
			f.msg = fmt.Sprintf("Stopped after buying %d of %d: %v", len(r.got), len(r.names), runErr)
		}
		f.hints = []string{"closedvps list shows them, and closedvps delete removes them."}
	}
	if len(unknown) > 0 {
		f.code = "partial"
		f.hints = append(f.hints, fmt.Sprintf("Couldn't check whether %s got bought after all, so they may be billing. Run closedvps list.", strings.Join(unknown, ", ")))
	}
	f.shown = true
	return f
}

// removeFirewalls deletes the firewalls this run made for servers it didn't buy, and names them.
func (r *createRun) removeFirewalls(keep map[string]bool) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var removed []string
	for _, n := range r.names {
		fw, ok := r.madeFW[n]
		if !ok || keep[n] {
			continue
		}
		if _, err := r.api.Firewall.Delete(ctx, fw); err == nil {
			removed = append(removed, fw.Name)
		}
	}
	return removed
}

// setUp waits for each server to start, turns on backups and checks its firewalls. A problem with one
// server doesn't stop the others; the problems come back as the first result.
func (r *createRun) setUp(ctx context.Context) ([]error, error) {
	var problems []error
	for _, p := range r.got {
		name := p.Server.Name
		if p.Action != nil {
			r.ui.wait(func() string { return "Starting " + name })
			err := r.api.Wait(ctx, p.Action)
			r.ui.done()
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				problems = append(problems, fmt.Errorf("Hetzner is still starting %s (%w); check the Hetzner Console", name, err))
				r.ui.warn("Hetzner is still starting %s: %v", name, err)
				r.ui.hint("Check it in the Hetzner Console.")
				continue
			}
		}
		if r.o.backups {
			if err := r.api.EnableBackups(ctx, p.Server); err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				r.ui.warn("Couldn't turn on backups for %s: %v", name, err)
				r.ui.hint("Turn them on in the Hetzner Console, on the server's Backups tab.")
			} else {
				r.ui.ok("Turned on backups for %s", name)
			}
		}
		open, err := r.api.OpenFirewalls(ctx, p.Server.ID)
		switch {
		case err != nil:
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			problems = append(problems, fmt.Errorf("couldn't check the firewalls on %s (%w); run closedvps list", name, err))
		case len(open) > 0:
			problems = append(problems, fmt.Errorf("other firewalls on %s let traffic in", name))
			r.ui.warn("Other firewalls on %s let traffic in: %s", name, strings.Join(open, ", "))
			r.ui.hint("Remove them in the Hetzner Console.")
		}
	}
	return problems, nil
}

// joined is what watching the tailnet found for one server.
type joined struct {
	host   string
	tagged bool
	sshUp  bool
}

// watch returns a look at the tailnet for a server, from this computer's Tailscale or the Tailscale API, or
// nil when neither is there.
func (r *createRun) watch() func(ctx context.Context, name string, created time.Time) (*joined, error) {
	if bin := r.app.tailscale(); bin != "" {
		return func(ctx context.Context, name string, created time.Time) (*joined, error) {
			peers, err := r.peers(ctx, bin)
			if err != nil {
				return nil, err
			}
			p := findPeer(peers, name, created)
			if p == nil || !p.Online || len(p.TailscaleIPs) == 0 {
				return nil, nil
			}
			host := strings.SplitN(p.DNSName, ".", 2)[0]
			if host == "" {
				host = p.TailscaleIPs[0]
			}
			return &joined{host: host, tagged: slices.Contains(p.Tags, tsapi.Tag), sshUp: r.sshUp(p.TailscaleIPs[0])}, nil
		}
	}
	if r.ts != nil {
		return func(ctx context.Context, name string, created time.Time) (*joined, error) {
			ds, err := r.ts.Devices(ctx)
			if err != nil {
				return nil, err
			}
			for _, d := range ds {
				if peerName(d.Name, name) && !d.Created.Before(created.Add(-time.Minute)) && d.Online {
					return &joined{host: d.Name, tagged: d.Tagged, sshUp: true}, nil
				}
			}
			return nil, nil
		}
	}
	return nil
}

// waitReady waits for each server to join the tailnet, then says what to do next. It returns a problem per
// server that didn't get there, or got there untagged.
func (r *createRun) waitReady(ctx context.Context) ([]error, error) {
	look := r.watch()
	if r.o.noWait || look == nil {
		if !r.o.noWait {
			who := "each server sends"
			if len(r.got) == 1 {
				who = r.got[0].Server.Name + " sends"
			}
			if len(r.rep.links) > 0 {
				r.ui.info("Can't see your tailnet from here; %s a \"ready\" alert when it joins", who)
			} else {
				r.ui.info("Can't see your tailnet from here; a server is ready once it shows up at login.tailscale.com/admin/machines")
			}
		}
		var hosts []string
		for _, p := range r.got {
			hosts = append(hosts, p.Server.Name)
		}
		r.whatNext(hosts)
		return nil, nil
	}
	var problems []error
	var ready []string
	for _, p := range r.got {
		name := p.Server.Name
		start := r.now()
		if r.watched {
			r.ui.wait(func() string {
				return fmt.Sprintf("Waiting for %s to join your tailnet · first boot takes 3-5 min · %s", name, since(r.now().Sub(start)))
			})
		} else {
			r.ui.info("Waiting for %s to join your tailnet (first boot takes 3-5 min)", name)
		}
		var j *joined
		for r.now().Sub(start) < 20*time.Minute {
			if got, err := look(ctx, name, p.Server.Created); err == nil && got != nil {
				j = got
				if got.sshUp {
					break
				}
			}
			if err := r.sleep(ctx, 10*time.Second); err != nil {
				r.ui.done()
				return nil, err
			}
		}
		r.ui.done()
		switch {
		case j == nil:
			problems = append(problems, fmt.Errorf("%s didn't join your tailnet", name))
			r.ui.fail("%s didn't join your tailnet within 20 minutes", name)
			r.ui.hint("The key may be wrong, expired or already used. %s is running and costs %s.", name, r.money(p.Candidate.Price))
			r.ui.hint("Delete it:    closedvps delete %s", name)
			r.ui.hint("Look inside:  closedvps open-ssh %s", name)
			ev := event{Event: "unreachable", Server: name, Reason: "didn't join the tailnet within 20 minutes"}
			r.rep.emit(ev)
			r.rep.alert(name+" isn't reachable", ev.Reason, ev, true)
			continue
		case !j.sshUp:
			problems = append(problems, fmt.Errorf("%s joined your tailnet but SSH isn't answering", name))
			r.ui.fail("%s joined your tailnet but SSH isn't answering yet", name)
			r.ui.hint("Try ssh %s@%s in a few minutes.", r.o.user, j.host)
			ev := event{Event: "unreachable", Server: name, Reason: "joined the tailnet but SSH isn't answering"}
			r.rep.emit(ev)
			r.rep.alert(name+" isn't reachable", ev.Reason, ev, true)
			continue
		case j.tagged:
			r.ui.ok("%s is on your tailnet, tagged tag:closedvps", j.host)
		default:
			problems = append(problems, fmt.Errorf("%s joined without tag:closedvps", name))
			r.ui.ok("%s is on your tailnet", j.host)
			r.ui.warn("%s has no tag, because your Tailscale policy doesn't define tag:closedvps", j.host)
			r.ui.hint("Until it's tagged, %s can open connections to your other devices.", j.host)
			r.ui.hint("Fix: add the blocks from closedvps.com/tailscale, then tag %s at login.tailscale.com/admin/machines", j.host)
		}
		ready = append(ready, j.host)
		ev := event{Event: "ready", Server: name, SSH: r.o.user + "@" + j.host, Tagged: new(j.tagged), Next: r.nextCommands(j.host)}
		r.rep.emit(ev)
		// The server sends this one to the alert links itself.
		r.rep.alert(name+" is ready", "ssh "+r.o.user+"@"+j.host, ev, false)
	}
	if len(problems) > 0 && r.rep.json != nil {
		code := "unreachable"
		if strings.Contains(problems[0].Error(), "without tag") {
			code = "untagged"
		}
		r.writeStopped(&failure{code: code, msg: errors.Join(problems...).Error()})
	}
	if len(ready) > 0 {
		if r.ts == nil && !r.saved {
			// Said on first runs, where the tailnet most likely still has the policy every tailnet starts with.
			r.ui.warn("closedvps can't read your Tailscale policy")
			r.ui.hint("If it still allows all traffic, %s can reach your devices: closedvps.com/tailscale", ready[0])
		}
		r.whatNext(ready)
	}
	return problems, nil
}

func (r *createRun) nextCommands(host string) []string {
	cmds := []string{"ssh " + r.o.user + "@" + host}
	if r.o.claude {
		cmds = append(cmds, "claude")
	}
	if r.o.codex {
		cmds = append(cmds, "codex login --device-auth")
	}
	return cmds
}

// whatNext is the Next: block: how to log in, sign in to the agents, and get rid of a test server.
func (r *createRun) whatNext(hosts []string) {
	var cmds [][2]string
	for _, h := range hosts {
		cmds = append(cmds, [2]string{"ssh " + r.o.user + "@" + h, ""})
	}
	if r.o.claude {
		cmds = append(cmds, [2]string{"claude", "then open the link it prints to sign in"})
	}
	if r.o.codex {
		cmds = append(cmds, [2]string{"codex login --device-auth", "then enter the code it prints at the link"})
	}
	if len(hosts) == 1 && !r.o.claude && !r.o.codex {
		cmds = append(cmds, [2]string{"closedvps delete " + hosts[0], "when you're done with it"})
	}
	r.ui.next(cmds)
	if r.o.claude || r.o.codex {
		who := "the servers"
		if len(hosts) == 1 {
			who = hosts[0]
		}
		r.ui.info("Updates can restart %s at 04:00 UTC, so run long sessions in tmux", who)
	}
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

func firstOfEachType(cs []buy.Candidate, n int) []buy.Candidate {
	var out []buy.Candidate
	seen := map[string]bool{}
	slices.SortStableFunc(cs, func(a, b buy.Candidate) int { return cmpFloat(a.Price, b.Price) })
	for _, c := range cs {
		if !seen[c.Type] && len(out) < n {
			seen[c.Type] = true
			out = append(out, c)
		}
	}
	return out
}

func priceList(cs []buy.Candidate) string {
	var s []string
	for _, c := range cs {
		s = append(s, fmt.Sprintf("%s (%.2f)", c.Type, c.Price))
	}
	return strings.Join(s, ", ")
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
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
			return fail("usage", fmt.Sprintf("Hetzner has no server type %q", w), "See closedvps types.")
		}
	}
	if len(locs) == 0 {
		return fail("usage", "--location is empty")
	}
	for _, l := range locs {
		if !knownLoc[l] {
			return fail("usage", fmt.Sprintf("Hetzner has no location %q", l), "See closedvps types.")
		}
	}
	return nil
}
