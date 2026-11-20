package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tulayha/closedvps/internal/hetznertest"
)

// A server that joins tagged is done; one that joins untagged is reachable but exits 3 with the untagged code,
// since until it's tagged it can reach the user's other devices.
func TestCreateWaitsForTheTailnet(t *testing.T) {
	for _, tc := range []struct {
		tags []string
		code int
		stop string
	}{
		{[]string{"tag:closedvps"}, exitOK, ""},
		{nil, exitBilling, "untagged"},
	} {
		_, url := hetznertest.New(t, &hetznertest.Type{Name: "cx23", Price: "4.00", InStock: hetznertest.Always, Left: -1})
		tr := newTestApp(t, url)
		created := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
		tr.app.tailscale = func() string { return "tailscale" }
		tr.app.sshUp = func(string) bool { return true }
		tr.app.peers = func(context.Context, string) ([]tsPeer, error) {
			return []tsPeer{{HostName: "vps", DNSName: "vps.tail1234.ts.net.", Online: true, TailscaleIPs: []string{"100.64.0.7"}, Created: created, Tags: tc.tags}}, nil
		}
		err := tr.run(context.Background(), "create", "--yes", "--json", "--location", "nbg1", "--user", "dev")
		if code := exitCode(err); code != tc.code {
			t.Fatalf("tags %v: exit %d (%v)\n%s", tc.tags, code, err, tr.stderr)
		}
		evs := tr.events(t)
		var ready *event
		for i, e := range evs {
			if e.Event == "ready" {
				ready = &evs[i]
			}
		}
		if ready == nil || ready.Tagged == nil || *ready.Tagged != (tc.tags != nil) || strings.Join(ready.Next, ",") != "ssh dev@vps" {
			t.Fatalf("tags %v: ready event %+v", tc.tags, ready)
		}
		if last := evs[len(evs)-1]; tc.stop != "" && (last.Event != "stopped" || last.Code != tc.stop || strings.Join(last.Billing, ",") != "vps") {
			t.Errorf("tags %v: last event %+v", tc.tags, last)
		}
		if got := strings.Contains(tr.stderr.String(), "vps has no tag"); got != (tc.tags == nil) {
			t.Errorf("tags %v: warning shown %v\n%s", tc.tags, got, tr.stderr)
		}
	}
}
