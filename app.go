package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"charm.land/huh/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/gen2brain/beeep"
	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"golang.org/x/term"

	"github.com/tulayha/closedvps/internal/cloud"
)

// app is everything a command touches outside its own logic, so tests can swap each part.
type app struct {
	stdout io.Writer // results: tables, JSON
	log    io.Writer // progress for people: stdout, or stderr with --json
	stderr io.Writer // questions, warnings, the bell
	in     *bufio.Reader
	tty    bool // stdin is a terminal, so questions can be asked
	// readSecret reads a line without echoing it.
	readSecret func() (string, error)

	endpoint  string                // Hetzner API; empty is the real one
	cloudOpts []hcloud.ClientOption // tests poll faster
	http      *http.Client          // alert links and the public IP lookup
	sleep     func(ctx context.Context, d time.Duration) error
	now       func() time.Time
	desktop   func(title, text string)
	tailscale func() string // the local Tailscale CLI, or "" without one
	peers     func(ctx context.Context, bin string) ([]tsPeer, error)
	sshUp     func(ip string) bool
	awake     func() func()                  // keeps the computer from sleeping; returns the release
	onTailnet func(ctx context.Context) bool // this computer runs Tailscale and is logged in
	keygen    func() error                   // runs ssh-keygen for a person at the terminal
	tsBase    string                         // Tailscale API; empty is the real one
	plain     bool                           // questions as plain numbered lines even in a terminal (tests)

	watched     bool          // stdout and stderr are terminals, so a person sees the update note
	installedBy string        // how this copy got here: fromRelease, fromGo, fromDocker or fromSource
	releases    string        // the latest-release API; empty is closedvps.com's
	self        string        // the binary upgrade replaces; empty is this one
	updateNote  func() string // set by startUpdateCheck
}

func newApp() *app {
	beeep.AppName = "closedvps"
	fd := int(os.Stdin.Fd())
	return &app{
		stdout: os.Stdout, log: os.Stdout, stderr: os.Stderr,
		in:  bufio.NewReader(os.Stdin),
		tty: term.IsTerminal(fd),
		readSecret: func() (string, error) {
			b, err := term.ReadPassword(fd)
			return string(b), err
		},
		http:      &http.Client{Timeout: 20 * time.Second},
		sleep:     sleep,
		now:       time.Now,
		desktop:   func(title, text string) { _ = beeep.Notify(title, text, "") },
		tailscale: tailscaleBinary,
		peers:     tailscalePeers,
		sshUp:     sshAnswers,
		awake:     keepAwake,
		onTailnet: tailscaleRunning,
		keygen:    sshKeygen,

		watched:     term.IsTerminal(int(os.Stdout.Fd())) && term.IsTerminal(int(os.Stderr.Fd())),
		installedBy: installedBy(),
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (a *app) client(token string) *cloud.Client {
	return cloud.New(token, currentVersion(), a.endpoint, nil, a.cloudOpts...)
}

// connect finds the token (HCLOUD_TOKEN, the saved one, or asks) and returns a client.
func (a *app) connect() (*cloud.Client, error) {
	t, err := a.hetznerToken()
	if err != nil {
		return nil, err
	}
	return a.client(t), nil
}

// askSecret asks for a secret without echoing it, as "? prompt".
func (a *app) askSecret(prompt string) (string, error) {
	if !a.tty {
		return "", errors.New("this question needs a terminal")
	}
	fmt.Fprintf(a.styled(), "%s %s ", cyan.Render("?"), prompt)
	v, err := a.readSecret()
	fmt.Fprintln(a.stderr)
	if err != nil {
		return "", err
	}
	if v = strings.TrimSpace(v); v == "" {
		return "", errors.New("nothing entered")
	}
	return v, nil
}

// styled is stderr with colour dropped where the terminal, NO_COLOR or TERM says so.
func (a *app) styled() io.Writer { return colorprofile.NewWriter(a.stderr, os.Environ()) }

// yesNo asks a yes/no question, with the default in capitals; Enter takes it, and without a terminal the
// answer is the default.
func (a *app) yesNo(q string, def bool) bool {
	if !a.tty {
		return def
	}
	hint := "(y/N)"
	if def {
		hint = "(Y/n)"
	}
	fmt.Fprintf(a.styled(), "%s %s %s ", cyan.Render("?"), q, hint)
	line, _ := a.in.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	case "n", "no":
		return false
	}
	return def
}

// choose asks for one of options and sets *choice to its index.
func (a *app) choose(q string, options []string, choice *int) error {
	var opts []huh.Option[int]
	for i, o := range options {
		opts = append(opts, huh.NewOption(o, i))
	}
	return a.question(huh.NewSelect[int]().Title(q).Options(opts...).Value(choice), func() string {
		return cyan.Render("?") + " " + q + " " + options[*choice]
	})
}

// question asks one thing with huh: its full prompt in a terminal, plain numbered lines otherwise (pipes,
// tests). Ctrl+C in the prompt stops the command like Ctrl+C anywhere else. huh clears a finished
// prompt, so record, if given, prints the answer to keep it on screen.
func (a *app) question(f huh.Field, record func() string) error {
	if !a.tty || a.plain {
		return f.RunAccessible(a.stderr, &lineReader{r: a.in})
	}
	err := huh.NewForm(huh.NewGroup(f)).WithOutput(a.stderr).Run()
	if errors.Is(err, huh.ErrUserAborted) {
		return context.Canceled
	}
	if err == nil && record != nil {
		fmt.Fprintln(a.styled(), record())
	}
	return err
}

// lineReader gives out one line per Read. huh's plain prompts each wrap the reader in a new bufio.Scanner,
// which would otherwise read ahead and swallow the answers to the questions after it.
type lineReader struct {
	r    *bufio.Reader
	rest []byte
}

func (l *lineReader) Read(p []byte) (int, error) {
	if len(l.rest) == 0 {
		line, err := l.r.ReadBytes('\n')
		if len(line) == 0 {
			return 0, err
		}
		l.rest = line
	}
	n := copy(p, l.rest)
	l.rest = l.rest[n:]
	return n, nil
}

// hetznerToken finds the token: HCLOUD_TOKEN, then the saved one, then it asks and offers to save it.
func (a *app) hetznerToken() (string, error) {
	if v := strings.TrimSpace(os.Getenv("HCLOUD_TOKEN")); v != "" {
		return v, nil
	}
	if t := tokenStore.get(); t != "" {
		return t, nil
	}
	if !a.tty {
		return "", fail("needs_key", "HCLOUD_TOKEN is not set",
			"Make a Read & Write token at console.hetzner.com > Security > API tokens.")
	}
	t, err := a.askSecret("Hetzner API token (console.hetzner.com > Security > API tokens, Read & Write)")
	if err != nil {
		return "", fail("needs_key", "No Hetzner token: "+err.Error())
	}
	if a.yesNo("Save it in your password store?", true) {
		u := newUI(a.stderr, false, a.now)
		if where, err := tokenStore.save(t); err != nil {
			u.warn("Couldn't save it: %v", err)
		} else {
			u.ok("Saved in %s; closedvps forget removes it", where)
		}
	}
	return t, nil
}

// tsCredentials is the Tailscale OAuth client: TS_OAUTH_CLIENT_ID and TS_OAUTH_CLIENT_SECRET, else the one
// setup saved. saved says which.
func tsCredentials() (id, secret string, saved bool) {
	id, secret = strings.TrimSpace(os.Getenv("TS_OAUTH_CLIENT_ID")), strings.TrimSpace(os.Getenv("TS_OAUTH_CLIENT_SECRET"))
	if id != "" && secret != "" {
		return id, secret, false
	}
	id, secret, _ = strings.Cut(tsClientStore.get(), "\n")
	if strings.TrimSpace(secret) == "" {
		return "", "", false
	}
	return strings.TrimSpace(id), strings.TrimSpace(secret), true
}
