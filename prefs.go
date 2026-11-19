package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/zalando/go-keyring"
)

// prefs are the answers from `closedvps setup`, reused by every create. Never secrets: those are in stores.
type prefs struct {
	Locations []string `json:"locations"`
	Types     []string `json:"types,omitempty"` // "cx23" or "cx23:6" with its own cap; empty: every shared type, cheapest first
	MaxPrice  float64  `json:"max_price"`
	Backups   bool     `json:"backups"`
	User      string   `json:"user"`
	SSHKey    string   `json:"ssh_key,omitempty"`
	MaxTotal  float64  `json:"max_total,omitempty"` // all closedvps servers in the project together; 0 is no limit
}

func defaultPrefs() prefs {
	return prefs{Locations: []string{"nbg1", "fsn1", "hel1"}, MaxPrice: 10, Backups: true, User: defaultUser()}
}

func configDir() (string, error) {
	if d := os.Getenv("CLOSEDVPS_CONFIG_DIR"); d != "" {
		return d, nil
	}
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "closedvps"), nil
}

// loadPrefs returns the saved preferences, or ok=false when setup hasn't run yet.
func loadPrefs() (prefs, bool, error) {
	dir, err := configDir()
	if err != nil {
		return defaultPrefs(), false, err
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return defaultPrefs(), false, nil
	}
	if err != nil {
		return defaultPrefs(), false, err
	}
	p := defaultPrefs()
	if err := json.Unmarshal(b, &p); err != nil {
		return defaultPrefs(), false, fmt.Errorf("reading %s: %w", filepath.Join(dir, "config.json"), err)
	}
	return p, true, nil
}

func savePrefs(p prefs) (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "config.json")
	return path, os.WriteFile(path, append(b, '\n'), 0o600)
}

// newNtfyLink is a fresh, hard-to-guess ntfy.sh topic.
func newNtfyLink() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "https://ntfy.sh/closedvps-" + hex.EncodeToString(b)
}

// A store keeps one secret in the system's password store (Keychain, Windows Credential Manager, the Secret
// Service keyring), or, where there is none such as a headless Linux box, in a file only the user can read.
type store struct{ name, what string }

var (
	tokenStore = store{"hetzner-token", "Hetzner token"}
	linksStore = store{"alert-links", "alert links"}
)

const keyringService = "closedvps"

func (s store) file() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, s.name), nil
}

func (s store) get() string {
	if t, err := keyring.Get(keyringService, s.name); err == nil && t != "" {
		return t
	}
	if p, err := s.file(); err == nil {
		if b, err := os.ReadFile(p); err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	return ""
}

// save reports where the secret went.
func (s store) save(v string) (string, error) {
	if err := keyring.Set(keyringService, s.name, v); err == nil {
		return "your system's password store", nil
	}
	p, err := s.file()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(p, []byte(v+"\n"), 0o600); err != nil {
		return "", err
	}
	// WriteFile keeps the mode of a file that already exists.
	if err := os.Chmod(p, 0o600); err != nil {
		return "", err
	}
	return p + " (readable only by you; this system has no password store)", nil
}

func (s store) forget() (bool, error) {
	removed := false
	if err := keyring.Delete(keyringService, s.name); err == nil {
		removed = true
	}
	if p, err := s.file(); err == nil {
		if err := os.Remove(p); err == nil {
			removed = true
		} else if !errors.Is(err, fs.ErrNotExist) {
			return removed, err
		}
	}
	return removed, nil
}
