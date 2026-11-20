// Package cloudinit renders the first-boot setup closedvps hands to Hetzner as user_data.
package cloudinit

import (
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"text/template"
	"unicode"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
	"golang.org/x/crypto/ssh"

	"github.com/tulayha/closedvps/internal/alert"
)

//go:embed cloud-init.yaml.tmpl
var tmplText string

// quote writes s as a YAML double-quoted scalar. The YAML library does the escaping, so nothing a user
// types can end the string early. Invalid UTF-8 would come out as !!binary, a different value, so it's refused.
func quote(s string) (string, error) {
	if !utf8.ValidString(s) {
		return "", errors.New("a value for the first-boot setup isn't valid text")
	}
	b, err := yaml.Marshal(&yaml.Node{Kind: yaml.ScalarNode, Style: yaml.DoubleQuotedStyle, Value: s})
	return strings.TrimSuffix(string(b), "\n"), err
}

var tmpl = template.Must(template.New("cloud-init").Funcs(template.FuncMap{
	"q":    quote,
	"curl": func(r alert.Request) (string, error) { return quote(r.CurlConfig()) },
}).Parse(tmplText))

// MaxSize is Hetzner's limit on user_data.
const MaxSize = 32 * 1024

type Config struct {
	Version      string
	Name         string
	User         string
	SSHPublicKey string
	TailscaleKey string
	TunnelToken  string
	Claude       bool
	Codex        bool
	// Ready are sent by the server once it has joined the tailnet, so the alert comes even when the computer
	// running closedvps can't watch the tailnet.
	Ready []alert.Request
}

var (
	hostnameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	userRe     = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	tokenRe    = regexp.MustCompile(`^[A-Za-z0-9+/=_.-]+$`)
)

var reservedUsers = map[string]bool{
	"root": true, "daemon": true, "bin": true, "sys": true, "sync": true, "games": true, "man": true,
	"lp": true, "mail": true, "news": true, "uucp": true, "proxy": true, "www-data": true, "backup": true,
	"list": true, "irc": true, "nobody": true, "systemd-network": true, "syslog": true, "sshd": true,
	"ubuntu": true, "admin": true, "sudo": true, "adm": true, "tailscale": true, "cloudflared": true, "nonroot": true,
}

func ValidName(s string) bool { return hostnameRe.MatchString(s) }

func ValidUser(s string) bool { return userRe.MatchString(s) && !reservedUsers[s] }

func (c Config) Validate() error {
	switch {
	case !ValidName(c.Name):
		return fmt.Errorf("server name %q: use lowercase letters, digits and dashes, up to 63 characters", c.Name)
	case !ValidUser(c.User):
		return fmt.Errorf("user name %q: use lowercase letters, digits, _ and -, starting with a letter, and not a system name like root or ubuntu", c.User)
	case !strings.HasPrefix(c.TailscaleKey, "tskey-") || !tokenRe.MatchString(c.TailscaleKey):
		return fmt.Errorf("the Tailscale auth key should start with tskey-")
	case c.TunnelToken != "" && !tokenRe.MatchString(c.TunnelToken):
		return fmt.Errorf("the Cloudflare tunnel token has characters a token can't have; paste only the token")
	case !validPublicKey(c.SSHPublicKey):
		return fmt.Errorf("the SSH public key should be one line like 'ssh-ed25519 AAAA... comment'")
	}
	for _, r := range c.Ready {
		if !strings.HasPrefix(r.URL, "https://") || hasControl(r.URL) {
			return errors.New("an alert link must be an https link without control characters")
		}
		for k, v := range r.Header {
			if hasControl(k + v) {
				return fmt.Errorf("alert header %q has a control character", k)
			}
		}
		if !utf8.ValidString(r.Body) {
			return errors.New("an alert message isn't valid text")
		}
	}
	return nil
}

// hasControl is true for invalid UTF-8 and for any control character, line breaks and DEL included.
func hasControl(s string) bool {
	return !utf8.ValidString(s) || strings.IndexFunc(s, unicode.IsControl) >= 0
}

func validPublicKey(k string) bool {
	if hasControl(k) {
		return false
	}
	_, _, opts, rest, err := ssh.ParseAuthorizedKey([]byte(k))
	return err == nil && len(opts) == 0 && len(rest) == 0
}

// Render returns the cloud-config document.
func Render(c Config) (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, c); err != nil {
		return "", err
	}
	if b.Len() > MaxSize {
		return "", fmt.Errorf("first-boot setup is %d bytes, over Hetzner's %d byte limit", b.Len(), MaxSize)
	}
	return b.String(), nil
}

// Redact hides the secrets in a rendered setup, for --dry-run output.
func Redact(doc string, c Config) string {
	hidden, _ := quote("<hidden: alert link and message>")
	for _, r := range c.Ready {
		if q, err := quote(r.CurlConfig()); err == nil {
			doc = strings.ReplaceAll(doc, q, hidden)
		}
	}
	for _, s := range []string{c.TailscaleKey, c.TunnelToken} {
		if s != "" {
			doc = strings.ReplaceAll(doc, s, "<hidden>")
		}
	}
	return doc
}
