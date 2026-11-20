package main

import (
	"context"
	"encoding/json"
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
	Tags         []string
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
		Self *tsPeer
		Peer map[string]tsPeer
	}
	if err := json.Unmarshal(out, &st); err != nil {
		return nil, err
	}
	peers := make([]tsPeer, 0, len(st.Peer)+1)
	if st.Self != nil {
		peers = append(peers, *st.Self) // list shows this computer too, when it's one of the servers
	}
	for _, p := range st.Peer {
		peers = append(peers, p)
	}
	return peers, nil
}

// findPeer picks the new server among the peers: named like it (Tailscale adds -1, -2 when the name is taken)
// and joined after it was created, so an old machine with the same name doesn't count.
// An exact name wins over a numbered one, so vps-2, another server, doesn't stand in for vps.
func findPeer(peers []tsPeer, name string, since time.Time) *tsPeer {
	var numbered *tsPeer
	for i, p := range peers {
		label := strings.SplitN(p.DNSName, ".", 2)[0]
		if p.Created.Before(since.Add(-time.Minute)) {
			continue
		}
		if strings.EqualFold(p.HostName, name) || label == name {
			return &peers[i]
		}
		if numbered == nil && (peerName(p.HostName, name) || peerName(label, name)) {
			numbered = &peers[i]
		}
	}
	return numbered
}

// peerName reports whether a tailnet name is the server's: Tailscale adds -1, -2 when the name is taken.
func peerName(label, name string) bool {
	return regexp.MustCompile(`^` + regexp.QuoteMeta(name) + `(-\d+)?$`).MatchString(strings.ToLower(label))
}

// tailscaleRunning reports whether this computer is logged in to a tailnet, so ssh to a new server works from here.
func tailscaleRunning(ctx context.Context) bool {
	bin := tailscaleBinary()
	if bin == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "status", "--json").Output()
	if err != nil {
		return false
	}
	var st struct{ BackendState string }
	return json.Unmarshal(out, &st) == nil && st.BackendState == "Running"
}

func sshAnswers(ip string) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort(ip, "22"), 5*time.Second)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}
