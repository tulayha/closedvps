package main

import (
	"bufio"
	"bytes"
	"context"
	"flag"
	"html"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/tulayha/closedvps/internal/hetznertest"
)

var update = flag.Bool("update", false, "rewrite testdata/example-run.txt")

// echo copies what's read to w, the way a terminal shows what you type.
type echo struct {
	r io.Reader
	w io.Writer
}

func (e echo) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	_, _ = e.w.Write(p[:n])
	return n, err
}

// exampleRun is a whole `closedvps create` in a terminal, with the prices of 19 Nov 2026: cx23 in nbg1 stays
// sold out, cax11 in hel1 comes into stock two and a half hours in, and the server joins the tailnet.
func exampleRun(t *testing.T) string {
	t.Helper()
	never := func(int) bool { return false }
	_, url := hetznertest.New(t,
		&hetznertest.Type{Name: "cx23", Location: "nbg1", Price: "5.49", InStock: never, Left: -1},
		&hetznertest.Type{Name: "cax11", Location: "hel1", Arch: "arm", Price: "5.99", InStock: hetznertest.From(3), Left: -1})
	tr := newTestApp(t, url)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLOSEDVPS_SSH_KEY", "")
	t.Setenv("CLOSEDVPS_NOTIFY", "")
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519.pub"), []byte(testKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := savePrefs(prefs{Locations: []string{"nbg1", "hel1"}, Types: []string{"cx23", "cax11"}, MaxPrice: 10, Backups: true, User: "dev"}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	a := tr.app
	a.stdout, a.log, a.stderr = &out, &out, &out
	a.tty = true
	a.in = bufio.NewReader(echo{strings.NewReader("y\n"), &out})
	clock := time.Date(2026, 11, 19, 9, 14, 2, 0, time.UTC)
	a.now = func() time.Time { return clock }
	a.sleep = func(_ context.Context, d time.Duration) error {
		if d == time.Minute {
			d = 149 * time.Minute // the wait for stock
		}
		clock = clock.Add(d)
		return nil
	}
	polls := 0
	a.tailscale = func() string { return "tailscale" }
	a.peers = func(context.Context, string) ([]tsPeer, error) {
		if polls++; polls < 13 {
			return nil, nil // still installing
		}
		return []tsPeer{{HostName: "vps", DNSName: "vps.tail1234.ts.net.", Online: true, TailscaleIPs: []string{"100.64.0.7"}, Created: clock}}, nil
	}
	a.sshUp = func(string) bool { return true }

	if err := tr.run(context.Background(), "create"); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	s := strings.ReplaceAll(out.String(), "\a", "")
	return strings.ReplaceAll(s, home, "~")
}

// testdata/example-run.txt is what a person sees in a whole run. The site's terminal is made from it
// (site/scripts/terminal.mjs), so the site never shows output the code doesn't print.
// go test . -run TestExampleRun -update rewrites it.
func TestExampleRun(t *testing.T) {
	got := exampleRun(t)
	const path = "testdata/example-run.txt"
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("the output changed; run go test . -run TestExampleRun -update, then npm run terminal in site/, and review both diffs. Now:\n%s", got)
	}
}

var dots = regexp.MustCompile(`\.{2,} online$`)

// Every closedvps line the site shows must be one the example run printed. The site stops quoting closedvps
// at its second command (nmap), and the "later" line marks the wait.
func TestSiteShowsRealOutput(t *testing.T) {
	b, err := os.ReadFile("testdata/example-run.txt")
	if err != nil {
		t.Fatal(err)
	}
	real := map[string]bool{}
	for line := range strings.Lines(string(b)) {
		real[dots.ReplaceAllString(strings.TrimRight(line, "\n"), "... online")] = true
	}
	check := func(where string, lines []string) {
		commands := 0
		for _, l := range lines {
			l = dots.ReplaceAllString(l, "... online")
			switch {
			case strings.HasPrefix(l, "$ "):
				commands++
			case commands != 1 || strings.HasSuffix(l, " later ...") || l == "":
			case !real[l]:
				t.Errorf("%s shows a line closedvps doesn't print: %q", where, l)
			}
		}
		if commands < 2 {
			t.Errorf("%s: no example terminal found", where)
		}
	}

	md, err := os.ReadFile("site/public/index.md")
	if err != nil {
		t.Fatal(err)
	}
	_, block, _ := strings.Cut(string(md), "```\n$ closedvps create")
	block, _, _ = strings.Cut("$ closedvps create"+block, "```")
	check("index.md", strings.Split(block, "\n"))

	page, err := os.ReadFile("site/public/index.html")
	if err != nil {
		t.Fatal(err)
	}
	pre := regexp.MustCompile(`(?s)<pre id="story"[^>]*>(.*?)</pre>`).FindSubmatch(page)
	if pre == nil {
		t.Fatal("no story terminal in index.html")
	}
	var lines []string
	for _, ln := range regexp.MustCompile(`(?s)<span class="ln">(.*?)</span>(?:<span class="ln">|$)`).FindAllSubmatch(append(pre[1], []byte(`<span class="ln">`)...), -1) {
		lines = append(lines, html.UnescapeString(regexp.MustCompile(`<[^>]+>`).ReplaceAllString(string(ln[1]), "")))
	}
	check("index.html", lines)
}
