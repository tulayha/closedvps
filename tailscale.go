package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
)

type tsPeer struct {
	HostName     string
	DNSName      string
	Online       bool
	TailscaleIPs []string
	Created      time.Time
}

// tailscaleBinary finds the local Tailscale CLI, which the macOS and Windows apps don't put on PATH.
func tailscaleBinary() string {
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p
	}
	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = []string{"/Applications/Tailscale.app/Contents/MacOS/Tailscale"}
	case "windows":
		candidates = []string{`C:\Program Files\Tailscale\tailscale.exe`}
	}
	for _, c := range candidates {
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	return ""
}

func tailscalePeers(ctx context.Context, bin string) ([]tsPeer, error) {
	out, err := exec.CommandContext(ctx, bin, "status", "--json").Output()
	if err != nil {
		return nil, err
	}
	var st struct {
		Peer map[string]tsPeer
	}
	if err := json.Unmarshal(out, &st); err != nil {
		return nil, err
	}
	peers := make([]tsPeer, 0, len(st.Peer))
	for _, p := range st.Peer {
		peers = append(peers, p)
	}
	return peers, nil
}

// findPeer picks the new server among the peers: named like it (Tailscale adds -1, -2 when the name is taken)
// and joined after it was created, so an old machine with the same name doesn't count.
func findPeer(peers []tsPeer, name string, since time.Time) *tsPeer {
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(name) + `(-\d+)?$`)
	for i, p := range peers {
		label := strings.SplitN(p.DNSName, ".", 2)[0]
		if (re.MatchString(strings.ToLower(p.HostName)) || re.MatchString(label)) && !p.Created.Before(since.Add(-time.Minute)) {
			return &peers[i]
		}
	}
	return nil
}

// waitForTailnet returns the server's name on the tailnet once SSH answers there.
func (a *app) waitForTailnet(ctx context.Context, bin, name, user string, created time.Time) (string, error) {
	fmt.Fprintf(a.log, "Waiting for %s to join your tailnet", name)
	deadline := a.now().Add(20 * time.Minute)
	var peer *tsPeer
	for a.now().Before(deadline) {
		if peers, err := a.peers(ctx, bin); err == nil {
			if p := findPeer(peers, name, created); p != nil && p.Online && len(p.TailscaleIPs) > 0 {
				peer = p
				if a.sshUp(p.TailscaleIPs[0]) {
					host := strings.SplitN(p.DNSName, ".", 2)[0]
					if host == "" {
						host = p.TailscaleIPs[0]
					}
					fmt.Fprintf(a.log, " online\nReady: ssh %s@%s\n", user, host)
					fmt.Fprintf(a.log, "In the Tailscale admin, turn off key expiry for %s, or it drops off your tailnet after 180 days.\n", host)
					return host, nil
				}
			}
		}
		fmt.Fprint(a.log, ".")
		if err := a.sleep(ctx, 10*time.Second); err != nil {
			fmt.Fprintln(a.log)
			return "", err
		}
	}
	fmt.Fprintln(a.log)
	if peer != nil {
		return "", fmt.Errorf("%s joined your tailnet but SSH isn't answering yet; try `ssh %s@%s` in a few minutes", name, user, name)
	}
	return "", fmt.Errorf("%s hasn't joined your tailnet after 20 minutes. Check the auth key in Tailscale's admin page; to look inside, `closedvps open-ssh %s` and read /var/log/cloud-init-output.log", name, name)
}

func sshAnswers(ip string) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort(ip, "22"), 5*time.Second)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}
