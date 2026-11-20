package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/tulayha/closedvps/internal/cloud"
	"github.com/tulayha/closedvps/internal/hetznertest"
)

// TestListEveryServer covers servers closedvps didn't make: one with no firewall, one whose firewall lets SSH in
// from anywhere, one behind a closed firewall, and one without a public IP.
func TestListEveryServer(t *testing.T) {
	fake, url := hetznertest.New(t, &hetznertest.Type{Name: "cx23", Location: "nbg1", Price: "5.49", InStock: hetznertest.Always, Left: -1})
	ssh := map[string]any{"direction": "in", "protocol": "tcp", "port": "22", "source_ips": []string{"0.0.0.0/0", "::/0"}, "destination_ips": []string{}}
	fake.Firewalls = []map[string]any{
		{"id": 101, "name": "closedvps-vps", "labels": cloud.Labels(), "rules": []any{}, "applied_to": []any{applied(1)}},
		{"id": 102, "name": "web", "labels": map[string]string{}, "rules": []any{ssh}, "applied_to": []any{applied(3)}},
		{"id": 103, "name": "shut", "labels": map[string]string{}, "rules": []any{}, "applied_to": []any{applied(4)}},
	}
	fake.Servers = []map[string]any{
		testServer(1, "vps", cloud.Labels(), true, 101),
		testServer(2, "bare", map[string]string{}, true),
		testServer(3, "web", map[string]string{}, true, 102),
		testServer(4, "shut", map[string]string{}, true, 103),
		testServer(5, "inside", map[string]string{}, false),
	}

	tr := newTestApp(t, url)
	if err := tr.run(context.Background(), "list", "--json"); err != nil {
		t.Fatal(err)
	}
	var rows []serverRow
	if err := json.Unmarshal(tr.stdout.Bytes(), &rows); err != nil {
		t.Fatalf("%v\n%s", err, tr.stdout)
	}
	want := map[string]string{
		"vps":    "closed",
		"bare":   "OPEN: no firewall",
		"web":    "OPEN: web: tcp 22 from 0.0.0.0/0,::/0",
		"shut":   "closed",
		"inside": "closed: no public IP",
	}
	for _, r := range rows {
		if r.Firewall != want[r.Name] {
			t.Errorf("%s: firewall %q, want %q", r.Name, r.Firewall, want[r.Name])
		}
		if r.Managed != (r.Name == "vps") {
			t.Errorf("%s: closedvps %v", r.Name, r.Managed)
		}
	}
	if len(rows) != len(want) {
		t.Errorf("%d rows, want %d", len(rows), len(want))
	}

	tr.stdout.Reset()
	if err := tr.run(context.Background(), "list"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tr.stdout.String(), "closedvps.com/guide#close-a-server-you-already-have") {
		t.Errorf("no pointer to the guide under OPEN servers:\n%s", tr.stdout)
	}
}

func applied(server int) map[string]any {
	return map[string]any{"type": "server", "server": map[string]any{"id": server}}
}

func testServer(id int, name string, labels map[string]string, public bool, firewalls ...int) map[string]any {
	fws := []any{}
	for _, f := range firewalls {
		fws = append(fws, map[string]any{"id": f, "status": "applied"})
	}
	var ipv4 any
	if public {
		ipv4 = map[string]any{"id": id, "ip": fmt.Sprintf("203.0.113.%d", id), "blocked": false}
	}
	return map[string]any{
		"id": id, "name": name, "status": "running", "created": "2026-11-19T09:00:00Z", "labels": labels,
		"server_type": map[string]any{"id": 1, "name": "cx23"}, "location": map[string]any{"id": 1, "name": "nbg1"}, "backup_window": nil,
		"public_net":  map[string]any{"ipv4": ipv4, "ipv6": nil, "firewalls": fws},
		"private_net": []any{}, "volumes": []any{}, "load_balancers": []any{},
	}
}
