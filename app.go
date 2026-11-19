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
	awake     func() func() // keeps the computer from sleeping; returns the release
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

// secret returns the value of env, or asks for it without echoing when running in a terminal.
func (a *app) secret(env, prompt, help string) (string, error) {
	if v := strings.TrimSpace(os.Getenv(env)); v != "" {
		return v, nil
	}
	if !a.tty {
		return "", fmt.Errorf("%s is not set (%s)", env, help)
	}
	return a.askSecret(prompt + "\n  " + help + "\n>")
}

func (a *app) askSecret(prompt string) (string, error) {
	if !a.tty {
		return "", errors.New("this question needs a terminal")
	}
	fmt.Fprintf(a.stderr, "%s ", prompt)
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

// confirm asks a yes/no question; Enter takes the default, and without a terminal the answer is the default.
func (a *app) confirm(q string, def bool) bool {
	if !a.tty {
		return def
	}
	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}
	fmt.Fprintf(a.stderr, "%s %s ", q, hint)
	line, _ := a.in.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	case "n", "no":
		return false
	}
	return def
}

// ask reads one line, with def used when the answer is empty.
func (a *app) ask(q, def string) string {
	if def != "" {
		fmt.Fprintf(a.stderr, "%s [%s] ", q, def)
	} else {
		fmt.Fprintf(a.stderr, "%s ", q)
	}
	line, _ := a.in.ReadString('\n')
	if line = strings.TrimSpace(line); line == "" {
		return def
	}
	return line
}

// hetznerToken finds the token: HCLOUD_TOKEN, then the saved one, then it asks and offers to save it.
func (a *app) hetznerToken() (string, error) {
	if v := strings.TrimSpace(os.Getenv("HCLOUD_TOKEN")); v != "" {
		return v, nil
	}
	if t := tokenStore.get(); t != "" {
		return t, nil
	}
	t, err := a.secret("HCLOUD_TOKEN", "Hetzner API token (Read & Write):",
		"Hetzner Console > your project > Security > API tokens. It's only ever sent to Hetzner.")
	if err != nil {
		return "", err
	}
	if a.confirm("Save it so you don't have to paste it again?", true) {
		where, err := tokenStore.save(t)
		if err != nil {
			fmt.Fprintf(a.stderr, "Couldn't save it: %v\n", err)
		} else {
			fmt.Fprintf(a.stderr, "Saved in %s. `closedvps forget` removes it.\n", where)
		}
	}
	return t, nil
}
