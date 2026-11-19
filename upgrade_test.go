package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeReleases serves GitHub's latest-release answer for tag, with this platform's archive holding bin.
type fakeReleases struct {
	asked atomic.Int32 // latest-release requests
	sums  string       // checksums.txt; a test can spoil it
}

func newFakeReleases(t *testing.T, tr *testRun, tagName string, bin []byte) *fakeReleases {
	t.Helper()
	name, archive := releaseArchive(t, bin)
	sum := sha256.Sum256(archive)
	f := &fakeReleases{sums: hex.EncodeToString(sum[:]) + "  " + name + "\n"}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/latest", func(w http.ResponseWriter, _ *http.Request) {
		f.asked.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": tagName,
			"html_url": "https://github.com/tulayha/closedvps/releases/tag/" + tagName,
			"assets": []map[string]string{
				{"name": name, "browser_download_url": srv.URL + "/dl/" + name},
				{"name": "checksums.txt", "browser_download_url": srv.URL + "/dl/checksums.txt"},
			},
		})
	})
	mux.HandleFunc("/dl/"+name, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) })
	mux.HandleFunc("/dl/checksums.txt", func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, f.sums) })
	tr.app.releases = srv.URL + "/latest"
	tr.app.http = srv.Client()
	return f
}

// releaseArchive is laid out like GoReleaser's: the binary at the top with LICENSE and README.md.
func releaseArchive(t *testing.T, bin []byte) (string, []byte) {
	t.Helper()
	files := []struct {
		name string
		body []byte
	}{{"LICENSE", []byte("MIT")}, {"README.md", []byte("# ClosedVPS")}, {"closedvps", bin}}
	var buf bytes.Buffer
	name := "closedvps_" + runtime.GOOS + "_" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		zw := zip.NewWriter(&buf)
		for _, f := range files {
			if f.name == "closedvps" {
				f.name += ".exe"
			}
			w, err := zw.Create(f.name)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write(f.body)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		return name + ".zip", buf.Bytes()
	}
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range files {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write(f.body)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return name + ".tar.gz", buf.Bytes()
}

// asRelease makes this test binary report v, as a release build would.
func asRelease(t *testing.T, tr *testRun, v string) {
	old := version
	version = v
	t.Cleanup(func() { version = old })
	tr.app.installedBy = fromRelease
}

func installedCopy(t *testing.T, tr *testRun) string {
	t.Helper()
	tr.app.self = filepath.Join(t.TempDir(), "closedvps")
	if err := os.WriteFile(tr.app.self, []byte("old binary"), 0o755); err != nil { //nolint:gosec // an executable
		t.Fatal(err)
	}
	return tr.app.self
}

func TestUpgradeReplacesTheBinary(t *testing.T) {
	tr := newTestApp(t, "")
	asRelease(t, tr, "0.1.0")
	newFakeReleases(t, tr, "v0.2.0", []byte("new binary"))
	self := installedCopy(t, tr)

	if err := tr.run(context.Background(), "upgrade"); err != nil {
		t.Fatalf("%v\n%s", err, tr.stdout)
	}
	if b, _ := os.ReadFile(self); string(b) != "new binary" {
		t.Fatalf("binary is %q", b)
	}
	want := "Updated closedvps from v0.1.0 to v0.2.0. What changed: https://github.com/tulayha/closedvps/releases/tag/v0.2.0"
	if !strings.Contains(tr.stdout.String(), want) {
		t.Fatalf("output:\n%s", tr.stdout)
	}
	left, _ := filepath.Glob(filepath.Join(filepath.Dir(self), "*"))
	if len(left) != 1 && runtime.GOOS != "windows" { // Windows keeps the old one, hidden, until the next upgrade
		t.Fatalf("left behind: %v", left)
	}
}

func TestUpgradeRefusesABadChecksum(t *testing.T) {
	tr := newTestApp(t, "")
	asRelease(t, tr, "0.1.0")
	f := newFakeReleases(t, tr, "v0.2.0", []byte("new binary"))
	f.sums = strings.Repeat("0", 64) + f.sums[64:]
	self := installedCopy(t, tr)

	err := tr.run(context.Background(), "upgrade")
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(self); string(b) != "old binary" {
		t.Fatalf("binary changed to %q", b)
	}
}

func TestUpgradeWhenNewest(t *testing.T) {
	tr := newTestApp(t, "")
	asRelease(t, tr, "0.2.0")
	newFakeReleases(t, tr, "v0.2.0", []byte("new binary"))
	self := installedCopy(t, tr)

	if err := tr.run(context.Background(), "upgrade"); err != nil {
		t.Fatal(err)
	}
	if got := tr.stdout.String(); got != "closedvps v0.2.0 is the newest release.\n" {
		t.Fatalf("output %q", got)
	}
	if b, _ := os.ReadFile(self); string(b) != "old binary" {
		t.Fatalf("binary changed to %q", b)
	}
}

func TestUpgradeElsewhere(t *testing.T) {
	for how, want := range map[string]string{
		fromDocker: "docker pull ghcr.io/tulayha/closedvps",
		fromGo:     "go install github.com/tulayha/closedvps@latest",
		fromSource: "development build",
	} {
		tr := newTestApp(t, "")
		f := newFakeReleases(t, tr, "v0.2.0", []byte("new binary"))
		tr.app.installedBy = how
		err := tr.run(context.Background(), "upgrade")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", how, err)
		}
		if f.asked.Load() != 0 {
			t.Errorf("%s: asked GitHub", how)
		}
	}
}

func TestUpdateNote(t *testing.T) {
	tr := newTestApp(t, "")
	asRelease(t, tr, "0.1.0")
	f := newFakeReleases(t, tr, "v0.2.0", []byte("new binary"))
	ctx := context.Background()

	if got, want := tr.app.checkForUpdate(ctx), "closedvps v0.2.0 is out (you have v0.1.0). Update with: closedvps upgrade"; got != want {
		t.Fatalf("note %q", got)
	}
	if got := tr.app.checkForUpdate(ctx); got != "" || f.asked.Load() != 1 {
		t.Fatalf("second check within a day: asked %d times, note %q", f.asked.Load(), got)
	}
	day := tr.app.now()
	tr.app.now = func() time.Time { return day.Add(25 * time.Hour) }
	if tr.app.checkForUpdate(ctx) == "" || f.asked.Load() != 2 {
		t.Fatalf("a day later: asked %d times", f.asked.Load())
	}

	tr.app.installedBy = fromGo
	tr.app.now = func() time.Time { return day.Add(50 * time.Hour) }
	if got := tr.app.checkForUpdate(ctx); !strings.HasSuffix(got, "Update with: go install github.com/tulayha/closedvps@latest") {
		t.Fatalf("go install note %q", got)
	}
}

// The note is for people at a terminal: never for agents, scripts, CI or Docker, and not when turned off.
func TestUpdateCheckOnlyWhenWatched(t *testing.T) {
	for name, c := range map[string]struct {
		args    []string
		env     string
		watched bool
		how     string
		want    bool
	}{
		"terminal":   {args: []string{"list"}, watched: true, want: true},
		"go install": {args: []string{"list"}, watched: true, how: fromGo, want: true},
		"json":       {args: []string{"list", "--json"}, watched: true},
		"yes":        {args: []string{"create", "--yes"}, watched: true},
		"no tty":     {args: []string{"list"}},
		"CI":         {args: []string{"list"}, env: "CI", watched: true},
		"turned off": {args: []string{"list"}, env: "CLOSEDVPS_NO_UPDATE_CHECK", watched: true},
		"docker":     {args: []string{"list"}, watched: true, how: fromDocker},
		"dev build":  {args: []string{"list"}, watched: true, how: fromSource},
		"upgrade":    {args: []string{"upgrade"}, watched: true},
	} {
		t.Run(name, func(t *testing.T) {
			tr := newTestApp(t, "")
			tr.app.watched = c.watched
			tr.app.installedBy = fromRelease
			if c.how != "" {
				tr.app.installedBy = c.how
			}
			t.Setenv("CI", "")
			t.Setenv("CLOSEDVPS_NO_UPDATE_CHECK", "")
			if c.env != "" {
				t.Setenv(c.env, "1")
			}
			cmd, rest, err := newRoot(tr.app).Find(c.args)
			if err == nil {
				err = cmd.ParseFlags(rest)
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := tr.app.wantsUpdateCheck(cmd); got != c.want {
				t.Fatalf("wantsUpdateCheck = %v", got)
			}
		})
	}
}
