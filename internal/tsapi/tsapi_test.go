package tsapi_test

import (
	"context"
	"testing"
	"time"

	"github.com/tulayha/closedvps/internal/tsapi"
	"github.com/tulayha/closedvps/internal/tsapi/tsapitest"
)

func TestCheckPolicy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		tag, open bool
		devices   bool
		want      tsapi.Policy
	}{
		{"no tag", false, false, true, tsapi.PolicyNoTag},
		{"closed", true, false, true, tsapi.PolicyOK},
		{"open", true, true, true, tsapi.PolicyOpen},
		{"nothing to test against", true, true, false, tsapi.PolicyOK},
	} {
		tn, url := tsapitest.New(t)
		tn.Tag, tn.Open = tc.tag, tc.open
		if tc.devices {
			tn.Devices = []map[string]any{tsapitest.Device("laptop", "2026-01-01T00:00:00Z", true)}
		}
		c, err := tsapi.New("id", "secret", url, "test")
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.CheckPolicy(context.Background())
		if err != nil || got != tc.want {
			t.Errorf("%s: got %v, %v; want %v", tc.name, got, err, tc.want)
		}
	}
}

func TestRefusedClient(t *testing.T) {
	tn, url := tsapitest.New(t)
	tn.Refuse = true
	c, _ := tsapi.New("id", "wrong", url, "test")
	if _, err := c.CheckPolicy(context.Background()); !tsapi.Refused(err) {
		t.Fatalf("a refused client should read as refused: %v", err)
	}
}

func TestNewKeyIsTagged(t *testing.T) {
	tn, url := tsapitest.New(t)
	c, _ := tsapi.New("id", "secret", url, "test")
	k, err := c.NewKey(context.Background(), "vps", time.Hour)
	if err != nil || k != "tskey-auth-made1" {
		t.Fatalf("got %q, %v", k, err)
	}
	tn.Lock(func() {
		if len(tn.Keys) != 1 || tn.Keys[0] != "closedvps vps tag:closedvps" {
			t.Fatalf("keys made: %v", tn.Keys)
		}
	})
}
