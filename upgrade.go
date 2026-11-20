package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/minio/selfupdate"
	"github.com/spf13/cobra"
	"golang.org/x/mod/semver"
)

// The site's Worker answers like GitHub's latest-release API and counts each ask, which is how the project
// knows how many copies are in use.
const latestReleaseAPI = "https://closedvps.com/latest"

// How this copy got here decides how it updates. The Dockerfile sets CLOSEDVPS_IN_DOCKER.
const (
	fromRelease = "release" // the install scripts or a release archive: it can replace itself
	fromGo      = "go"      // go install
	fromDocker  = "docker"
	fromSource  = "source" // a dev build, which has no version to compare
)

func installedBy() string {
	switch {
	case os.Getenv("CLOSEDVPS_IN_DOCKER") != "":
		return fromDocker
	case version != "dev":
		return fromRelease
	case currentVersion() != "dev":
		return fromGo
	}
	return fromSource
}

type release struct {
	Tag    string `json:"tag_name"`
	Page   string `json:"html_url"`
	Assets []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// latestRelease asks for the newest release; purpose is "check" or "upgrade".
func (a *app) latestRelease(ctx context.Context, purpose string) (release, error) {
	url := a.releases
	if url == "" {
		url = latestReleaseAPI
	}
	body, err := a.fetch(ctx, url+"?for="+purpose, 1<<20, "application/json")
	if err != nil {
		return release{}, err
	}
	var r release
	if err := json.Unmarshal(body, &r); err != nil {
		return release{}, fmt.Errorf("reading the newest release: %w", err)
	}
	if !semver.IsValid(r.Tag) {
		return release{}, fmt.Errorf("the newest release has no version (%q)", r.Tag)
	}
	return r, nil
}

func (a *app) fetch(ctx context.Context, url string, limit int64, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "closedvps/"+currentVersion()+" ("+runtime.GOOS+"/"+runtime.GOARCH+")")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", req.URL.Host, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err == nil && int64(len(b)) > limit {
		err = fmt.Errorf("%s is larger than expected", url)
	}
	return b, err
}

// tag turns a version as the build reports it (0.1.0, or v0.1.0 from go install) into a release tag.
func tag(v string) string {
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return v
}

func newer(latest, current string) bool {
	return semver.IsValid(tag(current)) && semver.Compare(latest, tag(current)) > 0
}

func upgradeHint(how string) string {
	if how == fromGo {
		return "go install github.com/tulayha/closedvps@latest"
	}
	return "closedvps upgrade"
}

func newUpgradeCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "upgrade",
		Short: "Replace this closedvps with the newest release",
		Long: "Downloads the newest release from GitHub, checks it against the release's checksums.txt, and puts " +
			"it in place of this one.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return a.upgrade(cmd.Context()) },
	}
}

func (a *app) upgrade(ctx context.Context) error {
	switch a.installedBy {
	case fromDocker:
		return errors.New("this is the Docker image; get the newest one with: docker pull ghcr.io/tulayha/closedvps")
	case fromGo:
		return errors.New("installed with go install; update with: " + upgradeHint(fromGo))
	case fromSource:
		return errors.New("this is a development build, so it can't tell which release is newer; install a release with the command on https://closedvps.com")
	}
	r, err := a.latestRelease(ctx, "upgrade")
	if err != nil {
		return fmt.Errorf("asking closedvps.com for the newest release: %w", err)
	}
	have := tag(currentVersion())
	if !newer(r.Tag, have) {
		fmt.Fprintf(a.stdout, "closedvps %s is the newest release.\n", have)
		return nil
	}
	target := a.self
	if target == "" {
		exe, err := os.Executable()
		if err == nil {
			target, err = filepath.EvalSymlinks(exe)
		}
		if err != nil {
			return fmt.Errorf("finding this closedvps: %w", err)
		}
	}
	opts := selfupdate.Options{TargetPath: target}
	if err := opts.CheckPermissions(); err != nil {
		return fmt.Errorf("can't write to %s (%w); run the install command from https://closedvps.com, which installs to a folder you own", filepath.Dir(target), err)
	}

	name := "closedvps_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	if runtime.GOOS == "windows" {
		name = strings.TrimSuffix(name, ".tar.gz") + ".zip"
	}
	fmt.Fprintf(a.log, "Downloading %s %s\n", name, r.Tag)
	archive, err := a.asset(ctx, r, name, 200<<20)
	if err != nil {
		return err
	}
	sums, err := a.asset(ctx, r, "checksums.txt", 1<<20)
	if err != nil {
		return err
	}
	if err := checkSum(archive, sums, name); err != nil {
		return err
	}
	bin, err := binaryIn(archive, name)
	if err != nil {
		return fmt.Errorf("reading %s: %w", name, err)
	}
	if err := selfupdate.Apply(bytes.NewReader(bin), opts); err != nil {
		if rerr := selfupdate.RollbackError(err); rerr != nil {
			return fmt.Errorf("replacing %s failed (%w), and so did putting the old one back (%w); run the install command from https://closedvps.com", target, err, rerr)
		}
		return fmt.Errorf("replacing %s: %w; the old version is still there", target, err)
	}
	fmt.Fprintf(a.stdout, "Updated closedvps from %s to %s. What changed: %s\n", have, r.Tag, r.Page)
	return nil
}

func (a *app) asset(ctx context.Context, r release, name string, limit int64) ([]byte, error) {
	for _, as := range r.Assets {
		if as.Name == name {
			b, err := a.fetch(ctx, as.URL, limit, "")
			if err != nil {
				return nil, fmt.Errorf("downloading %s: %w", name, err)
			}
			return b, nil
		}
	}
	return nil, fmt.Errorf("release %s has no %s", r.Tag, name)
}

// checkSum is the same check the install scripts make: the archive's SHA-256 against checksums.txt.
func checkSum(archive, sums []byte, name string) error {
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		want, file, ok := strings.Cut(sc.Text(), "  ")
		if !ok || file != name {
			continue
		}
		got := sha256.Sum256(archive)
		if hex.EncodeToString(got[:]) != want {
			return fmt.Errorf("checksum mismatch for %s; not installing it", name)
		}
		return nil
	}
	return fmt.Errorf("%s is not listed in checksums.txt; not installing it", name)
}

func binaryIn(archive []byte, name string) ([]byte, error) {
	if strings.HasSuffix(name, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if f.Name == "closedvps.exe" {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(io.LimitReader(rc, 200<<20))
			}
		}
		return nil, errors.New("no closedvps.exe inside")
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, errors.New("no closedvps inside")
			}
			return nil, err
		}
		if h.Name == "closedvps" && h.Typeflag == tar.TypeReg {
			return io.ReadAll(io.LimitReader(tr, 200<<20))
		}
	}
}

type updateCheck struct {
	Checked time.Time `json:"checked"`
	Latest  string    `json:"latest"`
}

// startUpdateCheck asks closedvps.com for the newest release while the command runs, at most once a day, as
// gh does. a.updateNote then gives the note to print, or "" when there's nothing new or the command ended
// before the answer came (the next command tries again).
func (a *app) startUpdateCheck(ctx context.Context, cmd *cobra.Command) {
	a.updateNote = func() string { return "" }
	if !a.wantsUpdateCheck(cmd) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	note := make(chan string, 1)
	go func() { note <- a.checkForUpdate(ctx) }()
	a.updateNote = func() string {
		cancel()
		return <-note
	}
}

// wantsUpdateCheck is true only where a person is watching: never for --json, --yes, CI, Docker or a
// dev build, and never during an upgrade, which does its own.
func (a *app) wantsUpdateCheck(cmd *cobra.Command) bool {
	if !a.watched || (a.installedBy != fromRelease && a.installedBy != fromGo) ||
		os.Getenv("CLOSEDVPS_NO_UPDATE_CHECK") != "" || os.Getenv("CI") != "" || cmd.Name() == "upgrade" {
		return false
	}
	for _, f := range []string{"json", "yes"} {
		if fl := cmd.Flags().Lookup(f); fl != nil && fl.Value.String() == "true" {
			return false
		}
	}
	return true
}

func (a *app) checkForUpdate(ctx context.Context) string {
	dir, err := configDir()
	if err != nil {
		return ""
	}
	path := filepath.Join(dir, "update-check.json")
	var last updateCheck
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &last)
	}
	if a.now().Sub(last.Checked) < 24*time.Hour {
		return ""
	}
	r, err := a.latestRelease(ctx, "check")
	if err != nil {
		return ""
	}
	if b, err := json.Marshal(updateCheck{Checked: a.now(), Latest: r.Tag}); err == nil && os.MkdirAll(dir, 0o700) == nil {
		_ = os.WriteFile(path, b, 0o600)
	}
	if !newer(r.Tag, currentVersion()) {
		return ""
	}
	return fmt.Sprintf("closedvps %s is out (you have %s). Update with: %s", r.Tag, tag(currentVersion()), upgradeHint(a.installedBy))
}
