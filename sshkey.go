package main

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/tulayha/closedvps/internal/cloudinit"
)

type publicKey struct {
	line        string // "ssh-ed25519 AAAA... comment", options dropped
	fingerprint string // MD5, the form Hetzner lists keys by
	from        string // where it came from, for messages
}

// findPublicKey uses --ssh-key when given, then CLOSEDVPS_SSH_KEY, then the saved or usual key file.
func findPublicKey(path string, flagGiven bool) (publicKey, error) {
	if env := strings.TrimSpace(os.Getenv("CLOSEDVPS_SSH_KEY")); env != "" && !flagGiven {
		return parsePublicKey(env, "the key in CLOSEDVPS_SSH_KEY")
	}
	if path == "" {
		var err error
		if path, err = defaultPublicKey(); err != nil {
			return publicKey{}, err
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return publicKey{}, err
	}
	return parsePublicKey(string(b), path)
}

func parsePublicKey(text, from string) (publicKey, error) {
	if strings.Contains(text, "PRIVATE KEY") {
		return publicKey{}, fmt.Errorf("%s is a private key; give the public one (the .pub file)", from)
	}
	pub, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(text))
	if err != nil {
		return publicKey{}, fmt.Errorf("%s isn't an SSH public key: %w", from, err)
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	if comment != "" {
		line += " " + comment
	}
	return publicKey{line: line, fingerprint: ssh.FingerprintLegacyMD5(pub), from: from}, nil
}

// defaultPublicKey finds the user's SSH public key in the usual places.
func defaultPublicKey() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	for _, n := range []string{"id_ed25519.pub", "id_ecdsa.pub", "id_rsa.pub"} {
		p := filepath.Join(home, ".ssh", n)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", errors.New("no SSH key found in ~/.ssh; make one with: ssh-keygen -t ed25519 (or pass --ssh-key)")
}

// defaultUser is the local login name when it works as a Linux user name, so `ssh <server>` just works.
func defaultUser() string {
	u, err := user.Current()
	if err != nil {
		return "dev"
	}
	name := strings.ToLower(u.Username)
	if i := strings.LastIndexAny(name, `\/`); i >= 0 { // Windows: DOMAIN\name
		name = name[i+1:]
	}
	name = strings.ReplaceAll(name, " ", "")
	if cloudinit.ValidUser(name) {
		return name
	}
	return "dev"
}
