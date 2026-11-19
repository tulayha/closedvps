package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestPrefsRoundTrip(t *testing.T) {
	t.Setenv("CLOSEDVPS_CONFIG_DIR", t.TempDir())
	if _, saved, err := loadPrefs(); err != nil || saved {
		t.Fatalf("fresh config: saved=%v err=%v", saved, err)
	}
	want := prefs{Locations: []string{"hel1"}, Types: []string{"cax11:6"}, MaxPrice: 7.5, User: "me", MaxTotal: 40}
	path, err := savePrefs(want)
	if err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); unixPerm(st) != 0o600 {
		t.Errorf("config mode %v", st.Mode().Perm())
	}
	got, saved, err := loadPrefs()
	if err != nil || !saved || got.MaxPrice != 7.5 || got.Backups || got.Types[0] != "cax11:6" || got.MaxTotal != 40 {
		t.Fatalf("got %+v saved=%v err=%v", got, saved, err)
	}
}

// Without a password store the secret goes to a file only the user can read, also when the file was
// already there with looser permissions.
func TestStoreFallsBackToFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLOSEDVPS_CONFIG_DIR", dir)
	keyring.MockInitWithError(errors.New("no keyring"))
	path := filepath.Join(dir, "hetzner-token")
	if err := os.WriteFile(path, []byte("old\n"), 0o640); err != nil { //nolint:gosec // looser on purpose: save must tighten it
		t.Fatal(err)
	}
	if _, err := tokenStore.save("tok"); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(path); err != nil || unixPerm(st) != 0o600 {
		t.Fatalf("token file: %v %v", st.Mode(), err)
	}
	if tokenStore.get() != "tok" {
		t.Fatal("saved token not read back")
	}
	if removed, err := tokenStore.forget(); err != nil || !removed || tokenStore.get() != "" {
		t.Fatalf("forget: %v %v", removed, err)
	}
}

func TestStoreInKeyring(t *testing.T) {
	t.Setenv("CLOSEDVPS_CONFIG_DIR", t.TempDir())
	keyring.MockInit()
	where, err := linksStore.save("https://ntfy.sh/x")
	if err != nil || where != "your system's password store" || linksStore.get() != "https://ntfy.sh/x" {
		t.Fatalf("%q %v", where, err)
	}
	if removed, _ := linksStore.forget(); !removed || linksStore.get() != "" {
		t.Fatal("forget didn't remove it")
	}
}

// unixPerm reads the mode bits, which Windows doesn't have: there the config sits in the user's own
// AppData folder, whose access list keeps other users out.
func unixPerm(st fs.FileInfo) fs.FileMode {
	if runtime.GOOS == "windows" {
		return 0o600
	}
	return st.Mode().Perm()
}
