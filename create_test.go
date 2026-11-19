package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/zalando/go-keyring"

	"github.com/tulayha/closedvps/internal/buy"
	"github.com/tulayha/closedvps/internal/hetznertest"
)

const testKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFg4qnGD3MSCr9AEVMn5vq89pMRtO3VRpbksUxnkH18/ me@laptop"

type testRun struct {
	app            *app
	stdout, stderr *bytes.Buffer
	mu             sync.Mutex
	alerts         []string // titles that reached the alert link
	desktop        []string // titles shown on this computer
}

// newTestApp is an app with no terminal, a fixed clock, instant sleeps and an alert link that records what
// it gets. The saved config and password store are empty.
func newTestApp(t *testing.T, endpoint string) *testRun {
	t.Helper()
	keyring.MockInit()
	tr := &testRun{stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	hook := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		var m struct{ Title string }
		_ = json.NewDecoder(r.Body).Decode(&m)
		tr.mu.Lock()
		tr.alerts = append(tr.alerts, m.Title)
		tr.mu.Unlock()
	}))
	t.Cleanup(hook.Close)
	tr.app = &app{
		stdout: tr.stdout, log: tr.stdout, stderr: tr.stderr,
		in:         bufio.NewReader(strings.NewReader("")),
		readSecret: func() (string, error) { return "", errors.New("no terminal") },
		endpoint:   endpoint, http: hook.Client(),
		cloudOpts: []hcloud.ClientOption{hcloud.WithPollOpts(hcloud.PollOpts{BackoffFunc: hcloud.ConstantBackoff(time.Millisecond)})},
		sleep:     func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
		now:       func() time.Time { return time.Date(2026, 11, 19, 9, 14, 2, 0, time.UTC) },
		desktop:   func(title, _ string) { tr.mu.Lock(); tr.desktop = append(tr.desktop, title); tr.mu.Unlock() },
		tailscale: func() string { return "" },
		awake:     func() func() { return func() {} },
	}
	t.Setenv("CLOSEDVPS_CONFIG_DIR", t.TempDir())
	t.Setenv("HCLOUD_TOKEN", "test-token")
	t.Setenv("TS_AUTHKEY", "tskey-auth-test")
	t.Setenv("CLOSEDVPS_SSH_KEY", testKey)
	t.Setenv("CLOSEDVPS_NOTIFY", hook.URL+"/hook")
	t.Setenv("CLOUDFLARE_TUNNEL_TOKEN", "")
	return tr
}

func (tr *testRun) run(ctx context.Context, args ...string) error {
	return run(ctx, tr.app, args)
}

// create runs a create the way an agent would: --yes --json, without waiting for the tailnet.
func (tr *testRun) create(ctx context.Context, args ...string) error {
	base := []string{"create", "--yes", "--json", "--no-wait", "--location", "nbg1", "--backups=false", "--user", "dev"}
	return tr.run(ctx, append(base, args...)...)
}

func (tr *testRun) events(t *testing.T) []event {
	t.Helper()
	var evs []event
	for line := range strings.Lines(tr.stdout.String()) {
		var e event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("stdout line isn't a JSON event: %q", line)
		}
		evs = append(evs, e)
	}
	return evs
}

func kinds(evs []event) string {
	var k []string
	for _, e := range evs {
		k = append(k, e.Event)
	}
	return strings.Join(k, " ")
}

func types(stock ...*hetznertest.Type) []*hetznertest.Type { return stock }

// The plan takes the first look at the stock. cx23 comes in on the third look with room for two servers,
// cx33 on the fourth.
func stockArrives() []*hetznertest.Type {
	return types(
		&hetznertest.Type{Name: "cx23", Price: "4.00", InStock: hetznertest.From(3), Left: 2},
		&hetznertest.Type{Name: "cx33", Price: "6.00", InStock: hetznertest.From(4), Left: -1},
	)
}

func TestCreateSeveralAsStockAppears(t *testing.T) {
	cloud, url := hetznertest.New(t, stockArrives()...)
	tr := newTestApp(t, url)
	if err := tr.create(context.Background(), "--count", "3", "--type", "cx23:6,cx33", "--max-total", "20"); err != nil {
		t.Fatalf("%v\n%s", err, tr.stderr)
	}
	evs := tr.events(t)
	if got := kinds(evs); got != "plan waiting bought bought waiting bought" {
		t.Fatalf("events: %s", got)
	}
	var bought []string
	for _, e := range evs {
		if e.Event == "bought" {
			bought = append(bought, fmt.Sprintf("%s=%s@%.2f #%d/%d", e.Server, e.Type, e.Price, *e.Bought, *e.Count))
		}
	}
	if got := strings.Join(bought, " "); got != "vps-1=cx23@4.50 #1/3 vps-2=cx23@4.50 #2/3 vps-3=cx33@6.50 #3/3" {
		t.Fatalf("bought: %s", got)
	}
	if w := evs[1]; *w.Bought != 0 || *w.Count != 3 {
		t.Fatalf("a waiting event must say 0 of 3 bought, not leave it out: %+v", w)
	}
	if strings.Join(tr.alerts, ",") != "vps-1 bought,vps-2 bought,vps-3 bought" {
		t.Fatalf("alerts: %v", tr.alerts)
	}
	cloud.Lock(func() {
		if ud := cloud.UserData["vps-3"]; !strings.Contains(ud, "ready-0.curl") || !strings.Contains(ud, "--hostname=vps-3") {
			t.Error("each server should carry its own name and the ready alert")
		}
		if len(cloud.Firewalls) != 3 || len(cloud.Deleted) != 0 || len(cloud.FailedPaths) != 0 {
			t.Errorf("firewalls %d, deleted %v, unanswered %v", len(cloud.Firewalls), cloud.Deleted, cloud.FailedPaths)
		}
	})
}

func TestCreateStopsAtTotalCap(t *testing.T) {
	cloud, url := hetznertest.New(t, stockArrives()...)
	tr := newTestApp(t, url)
	err := tr.create(context.Background(), "--count", "3", "--type", "cx23,cx33", "--max-total", "10")
	if exitCode(err) != exitBilling || !errors.Is(err, buy.ErrOverTotal) {
		t.Fatalf("want a billing error from the total cap, got %v", err)
	}
	evs := tr.events(t)
	if got := kinds(evs); got != "plan waiting bought bought waiting stopped" {
		t.Fatalf("events: %s", got)
	}
	if last := evs[len(evs)-1]; *last.Bought != 2 || *last.Count != 3 {
		t.Fatalf("stopped event: %+v", last)
	}
	if n := len(tr.alerts); n != 3 || tr.alerts[2] != "closedvps stopped" {
		t.Fatalf("alerts: %v", tr.alerts)
	}
	cloud.Lock(func() {
		if strings.Join(cloud.Deleted, ",") != "closedvps-vps-3" {
			t.Fatalf("only the unused third firewall should be deleted, deleted %v", cloud.Deleted)
		}
	})
}

// Ctrl+C after a purchase must exit 3, not 130: the server is billing.
func TestCreateStoppedAfterPurchaseIsBilling(t *testing.T) {
	_, url := hetznertest.New(t, &hetznertest.Type{Name: "cx23", Price: "4.00", InStock: hetznertest.Always, Left: 1})
	tr := newTestApp(t, url)
	ctx, cancel := context.WithCancel(context.Background())
	tr.app.sleep = func(context.Context, time.Duration) error { cancel(); return context.Canceled }
	err := tr.create(ctx, "--count", "2")
	if code := exitCode(err); code != exitBilling {
		t.Fatalf("exit %d (%v), want %d", code, err, exitBilling)
	}
	if !strings.Contains(tr.stderr.String(), "Still running and billing: vps-1") {
		t.Fatalf("log:\n%s", tr.stderr)
	}
}

// One server failing to start must not keep the others from being set up.
func TestCreateOneFailedStartSetsUpTheRest(t *testing.T) {
	cloud, url := hetznertest.New(t, &hetznertest.Type{Name: "cx23", Price: "4.00", InStock: hetznertest.Always, Left: -1})
	cloud.FailActions = map[int]bool{1: true} // vps-1's create action
	tr := newTestApp(t, url)
	err := tr.create(context.Background(), "--count", "2", "--backups=true")
	if exitCode(err) != exitBilling || !strings.Contains(err.Error(), "vps-1") {
		t.Fatalf("got %v", err)
	}
	cloud.Lock(func() {
		if strings.Join(cloud.BackupsOn, ",") != "vps-2" {
			t.Fatalf("backups turned on for %v, want vps-2 only", cloud.BackupsOn)
		}
	})
	if !strings.Contains(tr.stderr.String(), "vps-2 is running at 203.0.113.2") {
		t.Fatalf("log:\n%s", tr.stderr)
	}
}

func TestCreateDryRunWithoutToken(t *testing.T) {
	tr := newTestApp(t, "http://127.0.0.1:1") // never called
	t.Setenv("HCLOUD_TOKEN", "")
	t.Setenv("CLOUDFLARE_TUNNEL_TOKEN", "eyJhIjoiYiJ9")
	if err := tr.run(context.Background(), "create", "--dry-run", "--user", "dev"); err != nil {
		t.Fatal(err)
	}
	out := tr.stdout.String()
	for _, want := range []string{"skips the stock check", "#cloud-config", "Cloudflare Tunnel", "<hidden: alert link and message>"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "/hook") || strings.Contains(out, "eyJhIjoiYiJ9") {
		t.Error("a secret is in the dry run output")
	}
}

func TestCreateRejectsBadOptions(t *testing.T) {
	_, url := hetznertest.New(t, stockArrives()...)
	for _, args := range [][]string{
		{"--max-price", "NaN"},
		{"--max-price", "0"},
		{"--max-price", "inf"},
		{"--max-total", "NaN"},
		{"--type", "cx23:nan"},
		{"--count", "0"},
		{"--every", "1s"},
		{"--type", "cx99"},
		{"--location", "mars1"},
	} {
		tr := newTestApp(t, url)
		if err := tr.create(context.Background(), args...); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}

func TestNotifyFlagWarns(t *testing.T) {
	tr := newTestApp(t, "http://127.0.0.1:1")
	t.Setenv("HCLOUD_TOKEN", "")
	_ = tr.run(context.Background(), "create", "--dry-run", "--user", "dev", "--notify", "https://ntfy.sh/closedvps-x")
	if !strings.Contains(tr.stderr.String(), "shell history") {
		t.Fatalf("no warning:\n%s", tr.stderr)
	}
}

func TestExitCodes(t *testing.T) {
	cases := map[error]int{
		nil:                                 exitOK,
		errors.New("x"):                     exitFailed,
		context.Canceled:                    exitStopped,
		billingError{context.Canceled}:      exitBilling,
		fmt.Errorf("w: %w", billingError{}): exitBilling,
		usageError{errors.New("bad flag")}:  exitUsage,
	}
	for err, want := range cases {
		if got := exitCode(err); got != want {
			t.Errorf("exitCode(%v) = %d, want %d", err, got, want)
		}
	}
	tr := newTestApp(t, "")
	if code := exitCode(tr.run(context.Background(), "nope")); code != exitUsage {
		t.Errorf("unknown command: exit %d", code)
	}
	if code := exitCode(tr.run(context.Background(), "create", "--nope")); code != exitUsage {
		t.Errorf("unknown flag: exit %d", code)
	}
}

func TestHelpGoesToStdout(t *testing.T) {
	tr := newTestApp(t, "")
	if err := tr.run(context.Background(), "create", "--help"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tr.stdout.String(), "Exit codes:") || tr.stderr.Len() != 0 {
		t.Fatalf("stdout %q, stderr %q", tr.stdout, tr.stderr)
	}
}

// The skill is what agents read, so it must say what the code does: the exit codes word for word and every
// event.
func TestSkillMatchesHelp(t *testing.T) {
	b, err := os.ReadFile("skills/closedvps/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	skill := string(b)
	_, codes, _ := strings.Cut(createLong, "Exit codes:")
	if !strings.Contains(skill, "Exit codes:"+codes) {
		t.Error("the exit codes in SKILL.md differ from `closedvps create --help`")
	}
	for _, k := range eventKinds {
		if !strings.Contains(skill, "`"+k+"`") {
			t.Errorf("SKILL.md doesn't mention the %s event", k)
		}
	}
}
