package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestUntaggedServerWarns(t *testing.T) {
	for _, tc := range []struct {
		tags []string
		warn bool
	}{
		{nil, true},
		{[]string{"tag:closedvps"}, false},
	} {
		var out bytes.Buffer
		created := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
		a := &app{
			log:   &out,
			now:   func() time.Time { return created },
			sleep: func(context.Context, time.Duration) error { return nil },
			sshUp: func(string) bool { return true },
			peers: func(context.Context, string) ([]tsPeer, error) {
				return []tsPeer{{HostName: "vps", DNSName: "vps.tail1234.ts.net.", Online: true, TailscaleIPs: []string{"100.64.0.7"}, Created: created, Tags: tc.tags}}, nil
			},
		}
		host, err := a.waitForTailnet(context.Background(), "tailscale", "vps", "dev", created)
		if err != nil || host != "vps" {
			t.Fatalf("tags %v: got %q, %v", tc.tags, host, err)
		}
		if got := strings.Contains(out.String(), "has no Tailscale tag"); got != tc.warn {
			t.Errorf("tags %v: warning shown %v, want %v\n%s", tc.tags, got, tc.warn, out.String())
		}
	}
}
