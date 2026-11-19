package cloud_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tulayha/closedvps/internal/cloud"
	"github.com/tulayha/closedvps/internal/hetznertest"
)

func TestPrices(t *testing.T) {
	fake, url := hetznertest.New(t, &hetznertest.Type{Name: "cx23", Price: "4.00", InStock: hetznertest.Always, Left: -1})
	fake.IPv4Price = "0.6100"
	p, err := cloud.New("t", "test", url, nil).Prices(context.Background())
	if err != nil || p.Currency != "EUR" || p.IPv4["nbg1"] != 0.61 {
		t.Fatalf("got %+v, %v", p, err)
	}
}

func TestEnsureFirewall(t *testing.T) {
	fake, url := hetznertest.New(t)
	api := cloud.New("t", "test", url, nil)
	ctx := context.Background()

	fw, created, err := api.EnsureFirewall(ctx, "closedvps-vps")
	if err != nil || !created || len(fw.Rules) != 0 || fw.Labels[cloud.ManagedLabel] != cloud.ManagedValue {
		t.Fatalf("new firewall: %+v created=%v err=%v", fw, created, err)
	}
	if again, created, err := api.EnsureFirewall(ctx, "closedvps-vps"); err != nil || created || again.ID != fw.ID {
		t.Fatalf("an empty, unattached firewall from an earlier run should be reused: %v %v", created, err)
	}

	fake.Lock(func() {
		fake.Firewalls[0]["applied_to"] = []any{map[string]any{"type": "server", "server": map[string]any{"id": 9}}}
	})
	if _, _, err := api.EnsureFirewall(ctx, "closedvps-vps"); err == nil || !strings.Contains(err.Error(), "attached") {
		t.Fatalf("a firewall another server uses must not be shared, got %v", err)
	}

	fake.Lock(func() {
		fake.Firewalls[0]["applied_to"] = []any{}
		fake.Firewalls[0]["rules"] = []any{map[string]any{"direction": "in", "protocol": "tcp", "port": "22", "source_ips": []string{"0.0.0.0/0"}}}
	})
	if _, _, err := api.EnsureFirewall(ctx, "closedvps-vps"); err == nil || !strings.Contains(err.Error(), "inbound") {
		t.Fatalf("an opened firewall must not be reused, got %v", err)
	}
}

// A pricing reply that crashes the client must come back as an error, not take closedvps down.
func TestPricesSurvivesClientPanic(t *testing.T) {
	price := `{"location": "nbg1", "price_monthly": {"net": "0.50", "gross": "0.50"}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"pricing": {"currency": "EUR", "floating_ips": [{"type": "ipv4", "prices": [%s]}],
			"primary_ips": [{"type": "ipv4", "prices": [%s]}, {"type": "ipv6", "prices": [%s]}]}}`, price, price, price)
	}))
	t.Cleanup(srv.Close)
	_, err := cloud.New("t", "test", srv.URL, nil).Prices(context.Background())
	if err == nil || !strings.Contains(err.Error(), "couldn't read Hetzner's prices") {
		t.Fatalf("want a clear error, got %v", err)
	}
}
