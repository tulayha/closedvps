package main

import (
	"bufio"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tulayha/closedvps/internal/hetznertest"
	"github.com/tulayha/closedvps/internal/tsapi/tsapitest"
)

func lastEvent(t *testing.T, tr *testRun) event {
	t.Helper()
	evs := tr.events(t)
	if len(evs) == 0 {
		t.Fatalf("no events\n%s", tr.stderr)
	}
	return evs[len(evs)-1]
}

// Every way a run stops before buying writes a stopped event with its code, and exits 1.
func TestCreateStopCodes(t *testing.T) {
	small := &hetznertest.Type{Name: "cpx02", Price: "4.00", Memory: 1, InStock: hetznertest.Always, Left: -1}
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, tr *testRun, c *hetznertest.Cloud)
		args  []string
		code  string
	}{
		{"no tailscale key", func(t *testing.T, _ *testRun, _ *hetznertest.Cloud) { t.Setenv("TS_AUTHKEY", "") }, nil, "needs_key"},
		{"no hetzner token", func(t *testing.T, _ *testRun, _ *hetznertest.Cloud) { t.Setenv("HCLOUD_TOKEN", "") }, nil, "needs_key"},
		{"name taken", func(_ *testing.T, _ *testRun, c *hetznertest.Cloud) {
			c.Servers = append(c.Servers, testServer(1, "vps", map[string]string{"managed-by": "closedvps"}, true))
		}, nil, "name_taken"},
		{"over the cap", nil, []string{"--max-price", "1"}, "over_cap"},
		{"no yes", nil, []string{"--yes=false"}, "needs_yes"},
		{"bad flag value", nil, []string{"--every", "1s"}, "usage"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, url := hetznertest.New(t, &hetznertest.Type{Name: "cx23", Price: "4.00", InStock: hetznertest.Always, Left: -1})
			tr := newTestApp(t, url)
			if tc.setup != nil {
				tc.setup(t, tr, c)
			}
			err := tr.create(context.Background(), tc.args...)
			if code := exitCode(err); code != exitFailed {
				t.Fatalf("exit %d (%v)", code, err)
			}
			if e := lastEvent(t, tr); e.Event != "stopped" || e.Code != tc.code || *e.Bought != 0 {
				t.Fatalf("last event %+v, want stopped with %s", e, tc.code)
			}
			if !strings.Contains(tr.stderr.String(), "✗") {
				t.Errorf("the failure wasn't printed:\n%s", tr.stderr)
			}
		})
	}
	t.Run("too small", func(t *testing.T) {
		_, url := hetznertest.New(t, small, &hetznertest.Type{Name: "cx23", Price: "5.49", InStock: hetznertest.Always, Left: -1})
		tr := newTestApp(t, url)
		err := tr.create(context.Background(), "--claude", "--type", "cpx02")
		if e := lastEvent(t, tr); exitCode(err) != exitFailed || e.Code != "too_small" {
			t.Fatalf("exit %d, last event %+v", exitCode(err), e)
		}
		for _, want := range []string{"✗ cpx02 has 1 GB and Claude Code needs 4 GB", "Under your 10 EUR/mo cap: cx23 (5.99)", "Try: closedvps create --claude --type cx23"} {
			if !strings.Contains(tr.stderr.String(), want) {
				t.Errorf("missing %q in:\n%s", want, tr.stderr)
			}
		}
	})
}

// In a terminal a taken default name moves on to the next free one; without one it stops (above).
func TestCreateNumbersATakenNameInATerminal(t *testing.T) {
	c, url := hetznertest.New(t, &hetznertest.Type{Name: "cx23", Price: "4.00", InStock: hetznertest.Always, Left: -1})
	c.Servers = append(c.Servers, testServer(1, "vps", map[string]string{"managed-by": "closedvps"}, true))
	tr := newTestApp(t, url)
	tr.app.tty, tr.app.plain = true, true
	answers(tr, "1") // what it's for: just a server
	if err := tr.run(context.Background(), "create", "-n", "--location", "nbg1", "--user", "dev"); err != nil {
		t.Fatalf("%v\n%s", err, tr.stderr)
	}
	if !strings.Contains(tr.stderr.String(), "- vps is taken, so this one is vps-2") || !strings.Contains(tr.stderr.String(), "Server     vps-2 ·") {
		t.Fatalf("log:\n%s", tr.stderr)
	}
}

// With an OAuth client the policy is checked before anything is bought, and each server gets its own key.
func TestCreateWithOAuthClient(t *testing.T) {
	t.Run("no tag", func(t *testing.T) {
		_, url := hetznertest.New(t, &hetznertest.Type{Name: "cx23", Price: "4.00", InStock: hetznertest.Always, Left: -1})
		tn, tsURL := tsapitest.New(t)
		tn.Tag = false
		tr := newTestApp(t, url)
		tr.app.tsBase = tsURL
		t.Setenv("TS_AUTHKEY", "")
		t.Setenv("TS_OAUTH_CLIENT_ID", "id")
		t.Setenv("TS_OAUTH_CLIENT_SECRET", "secret")
		err := tr.create(context.Background())
		if e := lastEvent(t, tr); exitCode(err) != exitFailed || e.Code != "no_tag" {
			t.Fatalf("exit %d, last event %+v", exitCode(err), e)
		}
	})
	t.Run("one-off keys", func(t *testing.T) {
		c, url := hetznertest.New(t, &hetznertest.Type{Name: "cx23", Price: "4.00", InStock: hetznertest.Always, Left: -1})
		tn, tsURL := tsapitest.New(t)
		tn.Open = true
		tn.Devices = []map[string]any{tsapitest.Device("laptop", "2026-01-01T00:00:00Z", true)}
		tr := newTestApp(t, url)
		tr.app.tsBase = tsURL
		t.Setenv("TS_AUTHKEY", "")
		t.Setenv("TS_OAUTH_CLIENT_ID", "id")
		t.Setenv("TS_OAUTH_CLIENT_SECRET", "secret")
		if err := tr.create(context.Background(), "--count", "2"); err != nil {
			t.Fatalf("%v\n%s", err, tr.stderr)
		}
		if e := tr.events(t)[0]; e.Event != "plan" || e.Tailscale != "open" {
			t.Fatalf("plan %+v", e)
		}
		if !strings.Contains(tr.stderr.String(), "lets tag:closedvps reach your devices") {
			t.Errorf("no warning about the open policy:\n%s", tr.stderr)
		}
		tn.Lock(func() {
			if strings.Join(tn.Keys, ";") != "closedvps vps-1 tag:closedvps;closedvps vps-2 tag:closedvps" {
				t.Errorf("keys made: %v", tn.Keys)
			}
		})
		c.Lock(func() {
			if !strings.Contains(c.UserData["vps-2"], "tskey-auth-made2") {
				t.Error("vps-2 should carry the second key")
			}
		})
	})
}

func TestDelete(t *testing.T) {
	managed := map[string]string{"managed-by": "closedvps"}
	newCloud := func(t *testing.T) (*hetznertest.Cloud, *testRun) {
		c, url := hetznertest.New(t, &hetznertest.Type{Name: "cx23", Price: "5.49", InStock: hetznertest.Always, Left: -1})
		c.Firewalls = []map[string]any{
			{"id": 101, "name": "closedvps-vps", "labels": managed, "rules": []any{}, "applied_to": []any{applied(1)}},
			{"id": 102, "name": "closedvps-cvtest", "labels": managed, "rules": []any{}, "applied_to": []any{}},
		}
		c.Servers = []map[string]any{testServer(1, "vps", managed, true, 101), testServer(2, "alpaca", map[string]string{}, true)}
		c.Images = []map[string]any{{"id": 9, "type": "snapshot", "status": "available", "image_size": 2.0, "created_from": map[string]any{"id": 1, "name": "vps"}}}
		return c, newTestApp(t, url)
	}

	t.Run("a server, typed name", func(t *testing.T) {
		c, tr := newCloud(t)
		tr.app.tty = true
		tr.app.plain = true
		tr.app.in = bufio.NewReader(strings.NewReader("vps\n"))
		if err := tr.run(context.Background(), "delete", "vps"); err != nil {
			t.Fatalf("%v\n%s", err, tr.stderr)
		}
		c.Lock(func() {
			if strings.Join(c.Gone, ",") != "vps" || strings.Join(c.Deleted, ",") != "closedvps-vps" {
				t.Fatalf("deleted servers %v, firewalls %v", c.Gone, c.Deleted)
			}
		})
		for _, want := range []string{"✓ Deleted vps and firewall closedvps-vps", "vps left 1 snapshot", "login.tailscale.com/admin/machines"} {
			if !strings.Contains(tr.stderr.String(), want) {
				t.Errorf("missing %q in:\n%s", want, tr.stderr)
			}
		}
	})
	t.Run("an agent, with --confirm", func(t *testing.T) {
		_, tr := newCloud(t)
		if err := tr.run(context.Background(), "delete", "vps", "--confirm", "vps", "--json"); err != nil {
			t.Fatalf("%v\n%s", err, tr.stderr)
		}
		if e := lastEvent(t, tr); e.Event != "deleted" || e.Server != "vps" || e.Firewall != "closedvps-vps" || e.Price != 5.99 {
			t.Fatalf("event %+v", e)
		}
	})
	t.Run("without a confirmation", func(t *testing.T) {
		c, tr := newCloud(t)
		if err := tr.run(context.Background(), "delete", "vps"); err == nil || !strings.Contains(err.Error(), "--confirm vps") {
			t.Fatalf("got %v", err)
		}
		if err := tr.run(context.Background(), "delete", "vps", "--confirm", "vpx"); err == nil {
			t.Fatal("a wrong confirmation deleted it")
		}
		c.Lock(func() {
			if len(c.Gone) != 0 {
				t.Fatalf("deleted %v", c.Gone)
			}
		})
	})
	t.Run("a server closedvps didn't make", func(t *testing.T) {
		c, tr := newCloud(t)
		if err := tr.run(context.Background(), "delete", "alpaca", "--confirm", "alpaca"); err == nil || !strings.Contains(err.Error(), "wasn't made by closedvps") {
			t.Fatalf("got %v", err)
		}
		c.Lock(func() {
			if len(c.Gone) != 0 {
				t.Fatalf("deleted %v", c.Gone)
			}
		})
	})
	t.Run("a leftover firewall", func(t *testing.T) {
		c, tr := newCloud(t)
		if err := tr.run(context.Background(), "delete", "cvtest", "--confirm", "cvtest"); err != nil {
			t.Fatalf("%v\n%s", err, tr.stderr)
		}
		c.Lock(func() {
			if strings.Join(c.Deleted, ",") != "closedvps-cvtest" || len(c.Gone) != 0 {
				t.Fatalf("deleted firewalls %v, servers %v", c.Deleted, c.Gone)
			}
		})
	})
	t.Run("list names the leftover", func(t *testing.T) {
		_, tr := newCloud(t)
		if err := tr.run(context.Background(), "list"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(tr.stderr.String(), "Firewall closedvps-cvtest has no server; closedvps delete cvtest removes it") {
			t.Fatalf("stderr:\n%s", tr.stderr)
		}
	})
}

// Ctrl+C while waiting for stock deletes the firewall it made, says nothing was bought, and exits 130.
func TestCreateCtrlCBeforeBuying(t *testing.T) {
	c, url := hetznertest.New(t, &hetznertest.Type{Name: "cx23", Price: "4.00", InStock: func(int) bool { return false }, Left: -1})
	tr := newTestApp(t, url)
	ctx, cancel := context.WithCancel(context.Background())
	tr.app.sleep = func(context.Context, time.Duration) error { cancel(); return context.Canceled }
	err := tr.create(ctx)
	if code := exitCode(err); code != exitStopped {
		t.Fatalf("exit %d (%v)", code, err)
	}
	for _, want := range []string{"! Nothing on your list is in stock", "Stopping... deleted firewall closedvps-vps", "- Nothing was bought"} {
		if !strings.Contains(tr.stderr.String(), want) {
			t.Errorf("missing %q in:\n%s", want, tr.stderr)
		}
	}
	if e := lastEvent(t, tr); e.Event != "stopped" || e.Code != "interrupted" {
		t.Errorf("last event %+v", e)
	}
	c.Lock(func() {
		if strings.Join(c.Deleted, ",") != "closedvps-vps" {
			t.Errorf("deleted %v", c.Deleted)
		}
	})
}
