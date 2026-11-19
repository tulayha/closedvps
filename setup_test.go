package main

import (
	"bufio"
	"context"
	"strings"
	"testing"

	"github.com/tulayha/closedvps/internal/hetznertest"
)

func answers(tr *testRun, lines ...string) {
	tr.app.in = bufio.NewReader(strings.NewReader(strings.Join(lines, "\n") + "\n"))
}

func TestSetupWizardSavesAnswers(t *testing.T) {
	_, url := hetznertest.New(t,
		&hetznertest.Type{Name: "cx23", Price: "4.00", InStock: hetznertest.Always},
		&hetznertest.Type{Name: "cax11", Price: "4.50", InStock: hetznertest.Always})
	tr := newTestApp(t, url)
	// location, an unknown type, a bad cap, good types, backups (Enter keeps yes), price, total, user, and
	// alerts by number (4 is None when nothing is saved).
	answers(tr, "3,nbg1", "cx99", "cx23:x", "cax11:6,cx23", "", "12", "30", "me", "4")
	p, err := tr.app.setupWizard(context.Background(), tr.app.client("t"), defaultPrefs())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.Locations, ",") != "hel1,nbg1" || strings.Join(p.Types, ",") != "cax11:6,cx23" || p.MaxPrice != 12 || p.MaxTotal != 30 || p.User != "me" || !p.Backups {
		t.Fatalf("got %+v", p)
	}
	saved, ok, err := loadPrefs()
	if err != nil || !ok || saved.User != "me" {
		t.Fatalf("not saved: %+v %v %v", saved, ok, err)
	}
	if !strings.Contains(tr.stderr.String(), `Hetzner has no type "cx99"`) {
		t.Errorf("typo not reported:\n%s", tr.stderr)
	}
}

// A Telegram step that fails (here: no terminal for the token) must leave the saved links alone.
func TestSetupAlertsFailedTelegramKeepsLinks(t *testing.T) {
	tr := newTestApp(t, "")
	if _, err := linksStore.save("https://ntfy.sh/closedvps-old"); err != nil {
		t.Fatal(err)
	}
	// With links saved the menu is 1 Keep, 2 ntfy, 3 Telegram, 4 Links, 5 None.
	answers(tr, "3")
	if err := tr.app.setupAlerts(context.Background()); err == nil {
		t.Fatal("want the Telegram error")
	}
	if got := linksStore.get(); got != "https://ntfy.sh/closedvps-old" {
		t.Fatalf("saved links now %q", got)
	}

	answers(tr, "4", "https://ntfy.sh/a, ntfy.sh/typo")
	if err := tr.app.setupAlerts(context.Background()); err == nil || linksStore.get() != "https://ntfy.sh/closedvps-old" {
		t.Fatalf("a bad link must be refused without touching the saved ones: %v, %q", err, linksStore.get())
	}

	answers(tr, "5")
	if err := tr.app.setupAlerts(context.Background()); err != nil || linksStore.get() != "" {
		t.Fatalf("none should delete them: %v, %q", err, linksStore.get())
	}
}

func TestPickLocations(t *testing.T) {
	_, url := hetznertest.New(t)
	tr := newTestApp(t, url)
	locs, err := tr.app.client("t").Locations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := pickLocations("2, hel1, 2", locs); err != nil || strings.Join(got, ",") != "nbg1,hel1" {
		t.Fatalf("got %v %v", got, err)
	}
	for _, bad := range []string{"9", "ash", ""} {
		if _, err := pickLocations(bad, locs); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
