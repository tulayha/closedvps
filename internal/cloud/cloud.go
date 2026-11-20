// Package cloud is what closedvps needs from Hetzner Cloud, on top of the official hcloud-go client: prices
// per month, the servers closedvps made, and their firewalls.
package cloud

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
)

const (
	ManagedLabel = "managed-by"
	ManagedValue = "closedvps"
	// BackupShare is what Hetzner Backups add to a server's price.
	BackupShare = 0.2
)

// Labels go on everything closedvps makes, so `list` finds it.
func Labels() map[string]string { return map[string]string{ManagedLabel: ManagedValue} }

func FirewallName(server string) string { return "closedvps-" + server }

func IsManaged(s *hcloud.Server) bool { return s != nil && s.Labels[ManagedLabel] == ManagedValue }

func IsManagedFirewall(fw *hcloud.Firewall) bool {
	return fw != nil && fw.Labels[ManagedLabel] == ManagedValue
}

type Client struct{ *hcloud.Client }

// New makes a client. endpoint and hc may be empty and nil for the real API; extra options go last, so
// tests can poll faster.
func New(token, version, endpoint string, hc *http.Client, extra ...hcloud.ClientOption) *Client {
	opts := []hcloud.ClientOption{
		hcloud.WithToken(token),
		hcloud.WithApplication("closedvps", version),
		hcloud.WithPollOpts(hcloud.PollOpts{BackoffFunc: hcloud.ConstantBackoff(2 * time.Second)}),
	}
	if endpoint != "" {
		opts = append(opts, hcloud.WithEndpoint(endpoint))
	}
	if hc != nil {
		opts = append(opts, hcloud.WithHTTPClient(hc))
	}
	return &Client{hcloud.NewClient(append(opts, extra...)...)}
}

// IsAPIError reports whether Hetzner answered with an error, as opposed to the request not getting an answer.
func IsAPIError(err error) bool {
	var e hcloud.Error
	return errors.As(err, &e)
}

// IsCode reports whether err is an API error with one of the given codes.
func IsCode(err error, codes ...string) bool {
	for _, c := range codes {
		if hcloud.IsError(err, hcloud.ErrorCode(c)) {
			return true
		}
	}
	return false
}

// Prices are the account's currency and the monthly IPv4 price per location, before VAT.
type Prices struct {
	Currency   string
	IPv4       map[string]float64
	SnapshotGB float64 // per GB per month
}

// Prices reads the IPv4 prices. hcloud-go v2.53.0 panics on a pricing reply with more primary IP types than
// floating IP types (fixed upstream in hetznercloud/hcloud-go#970), so a panic comes back as an error.
func (c *Client) Prices(ctx context.Context) (_ Prices, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("couldn't read Hetzner's prices: the hcloud-go client failed on their reply (%v); a newer closedvps may fix it", r)
		}
	}()
	p, _, err := c.Pricing.Get(ctx)
	if err != nil {
		return Prices{}, err
	}
	out := Prices{Currency: p.Currency, IPv4: map[string]float64{}}
	out.SnapshotGB, _ = strconv.ParseFloat(p.Image.PerGBMonth.Net, 64)
	for _, ip := range p.PrimaryIPs {
		if ip.Type != "ipv4" {
			continue
		}
		for _, pr := range ip.Pricings {
			if v, err := strconv.ParseFloat(pr.Monthly.Net, 64); err == nil {
				out.IPv4[pr.Location] = v
			}
		}
	}
	return out, nil
}

// ServerPrice is a type's monthly price in a location, before VAT, without IPv4 or backups.
func ServerPrice(t *hcloud.ServerType, location string) (float64, bool) {
	if t == nil {
		return 0, false
	}
	for _, p := range t.Pricings {
		if p.Location != nil && p.Location.Name == location {
			v, err := strconv.ParseFloat(p.Monthly.Net, 64)
			return v, err == nil
		}
	}
	return 0, false
}

func (c *Client) ServerTypes(ctx context.Context) ([]*hcloud.ServerType, error) {
	return c.ServerType.All(ctx)
}

func (c *Client) Locations(ctx context.Context) ([]*hcloud.Location, error) {
	return c.Location.All(ctx)
}

func (c *Client) Create(ctx context.Context, o hcloud.ServerCreateOpts) (hcloud.ServerCreateResult, error) {
	res, _, err := c.Server.Create(ctx, o)
	return res, err
}

func (c *Client) ServerByName(ctx context.Context, name string) (*hcloud.Server, error) {
	s, _, err := c.Server.GetByName(ctx, name)
	return s, err
}

// Servers lists every server in this project.
func (c *Client) Servers(ctx context.Context) ([]*hcloud.Server, error) {
	return c.Server.All(ctx)
}

// Managed lists the servers closedvps made in this project.
func (c *Client) Managed(ctx context.Context) ([]*hcloud.Server, error) {
	return c.Server.AllWithOpts(ctx, hcloud.ServerListOpts{
		ListOpts: hcloud.ListOpts{LabelSelector: ManagedLabel + "=" + ManagedValue},
	})
}

func (c *Client) Wait(ctx context.Context, actions ...*hcloud.Action) error {
	return c.Action.WaitFor(ctx, actions...)
}

// EnableBackups turns on Hetzner Backups (7 daily copies) and waits for it.
func (c *Client) EnableBackups(ctx context.Context, s *hcloud.Server) error {
	act, _, err := c.Server.EnableBackup(ctx, s, "")
	if err != nil {
		return err
	}
	return c.Wait(ctx, act)
}

// EnsureSSHKey returns the project's key with this MD5 fingerprint, adding it under name when it isn't there.
func (c *Client) EnsureSSHKey(ctx context.Context, fingerprint, publicKey, name string) (*hcloud.SSHKey, error) {
	k, _, err := c.SSHKey.GetByFingerprint(ctx, fingerprint)
	if err != nil || k != nil {
		return k, err
	}
	k, _, err = c.SSHKey.Create(ctx, hcloud.SSHKeyCreateOpts{Name: name, PublicKey: publicKey, Labels: Labels()})
	return k, err
}

// InboundRules are the rules that let traffic in. With none, Hetzner drops all inbound traffic.
func InboundRules(fw *hcloud.Firewall) []hcloud.FirewallRule {
	var in []hcloud.FirewallRule
	for _, r := range fw.Rules {
		if r.Direction == hcloud.FirewallRuleDirectionIn {
			in = append(in, r)
		}
	}
	return in
}

func AppliedToServer(fw *hcloud.Firewall, id int64) bool {
	for _, r := range fw.AppliedTo {
		if r.Type == hcloud.FirewallResourceTypeServer && r.Server != nil && r.Server.ID == id {
			return true
		}
	}
	return false
}

// EnsureFirewall returns a firewall with no rules for a new server, creating it if needed. An existing one is
// used only when it has no inbound rules and isn't attached to anything, so a firewall someone opened, or
// one another server uses, never ends up on the new server.
func (c *Client) EnsureFirewall(ctx context.Context, name string) (fw *hcloud.Firewall, created bool, err error) {
	fw, _, err = c.Firewall.GetByName(ctx, name)
	if err != nil {
		return nil, false, err
	}
	if fw != nil {
		if in := InboundRules(fw); len(in) > 0 {
			return nil, false, fmt.Errorf("firewall %s already exists with %d inbound rules; remove them or delete it in the Hetzner Console", name, len(in))
		}
		if len(fw.AppliedTo) > 0 {
			return nil, false, fmt.Errorf("firewall %s already exists and is attached to another server; pick another --name or delete it in the Hetzner Console", name)
		}
		return fw, false, nil
	}
	res, _, err := c.Firewall.Create(ctx, hcloud.FirewallCreateOpts{Name: name, Labels: Labels(), Rules: []hcloud.FirewallRule{}})
	if err != nil {
		return nil, false, err
	}
	return res.Firewall, true, nil
}

// OpenFirewalls describes each firewall on the server that lets traffic in. Hetzner adds up the inbound rules
// of every firewall on a server, so one open firewall opens it.
func (c *Client) OpenFirewalls(ctx context.Context, serverID int64) ([]string, error) {
	srv, _, err := c.Server.GetByID(ctx, serverID)
	if err != nil {
		return nil, err
	}
	if srv == nil {
		return nil, fmt.Errorf("server %d is gone", serverID)
	}
	var open []string
	for _, ref := range srv.PublicNet.Firewalls {
		fw, _, err := c.Firewall.GetByID(ctx, ref.Firewall.ID)
		if err != nil {
			return nil, err
		}
		if fw == nil {
			continue
		}
		var parts []string
		for _, r := range InboundRules(fw) {
			parts = append(parts, DescribeRule(r))
		}
		if len(parts) > 0 {
			open = append(open, fw.Name+": "+strings.Join(parts, ", "))
		}
	}
	return open, nil
}

func DescribeRule(r hcloud.FirewallRule) string {
	var from []string
	for _, n := range r.SourceIPs {
		from = append(from, n.String())
	}
	s := string(r.Protocol)
	if r.Port != nil {
		s += " " + *r.Port
	}
	return s + " from " + strings.Join(from, ",")
}

// FirewallState says whether the server's closedvps firewall is attached and whether anything lets traffic in.
func (c *Client) FirewallState(ctx context.Context, s *hcloud.Server) (string, error) {
	fw, _, err := c.Firewall.GetByName(ctx, FirewallName(s.Name))
	if err != nil {
		return "", err
	}
	if fw == nil || !AppliedToServer(fw, s.ID) {
		return "MISSING: the server has no closedvps firewall", nil
	}
	open, err := c.OpenFirewalls(ctx, s.ID)
	if err != nil {
		return "", err
	}
	if len(open) == 0 {
		return "closed", nil
	}
	return "OPEN: " + strings.Join(open, "; "), nil
}

// Exposure says whether the internet can reach a server: FirewallState for closedvps servers, and for others
// whether any firewall is attached and what its inbound rules let in. Without a firewall every port the server
// listens on is open.
func (c *Client) Exposure(ctx context.Context, s *hcloud.Server) (string, error) {
	if IsManaged(s) {
		return c.FirewallState(ctx, s)
	}
	if IPv4(s) == "" && (s.PublicNet.IPv6.IP == nil || s.PublicNet.IPv6.IP.IsUnspecified()) {
		return "closed: no public IP", nil
	}
	if len(s.PublicNet.Firewalls) == 0 {
		return "OPEN: no firewall", nil
	}
	open, err := c.OpenFirewalls(ctx, s.ID)
	if err != nil {
		return "", err
	}
	if len(open) == 0 {
		return "closed", nil
	}
	return "OPEN: " + strings.Join(open, "; "), nil
}

// ServerFirewall finds a closedvps server and its own firewall, for open-ssh and close-ssh.
func (c *Client) ServerFirewall(ctx context.Context, name string) (*hcloud.Server, *hcloud.Firewall, error) {
	srv, err := c.ServerByName(ctx, name)
	if err != nil {
		return nil, nil, err
	}
	if srv == nil {
		return nil, nil, fmt.Errorf("no server named %q in this project", name)
	}
	fw, _, err := c.Firewall.GetByName(ctx, FirewallName(name))
	if err != nil {
		return nil, nil, err
	}
	if fw == nil || !AppliedToServer(fw, srv.ID) {
		return nil, nil, fmt.Errorf("%s has no %s firewall; it wasn't made by closedvps", name, FirewallName(name))
	}
	return srv, fw, nil
}

// SetRules replaces a firewall's rules and waits until Hetzner has applied them.
func (c *Client) SetRules(ctx context.Context, fw *hcloud.Firewall, rules []hcloud.FirewallRule) error {
	if rules == nil {
		rules = []hcloud.FirewallRule{}
	}
	acts, _, err := c.Firewall.SetRules(ctx, fw, hcloud.FirewallSetRulesOpts{Rules: rules})
	if err != nil {
		return err
	}
	return c.Wait(ctx, acts...)
}

func IPv4(s *hcloud.Server) string {
	if s == nil || s.PublicNet.IPv4.IP == nil || s.PublicNet.IPv4.IP.IsUnspecified() {
		return ""
	}
	return s.PublicNet.IPv4.IP.String()
}

// Inbound is every rule that lets traffic in to a server, from all its firewalls. firewalls is how many are on it.
func (c *Client) Inbound(ctx context.Context, s *hcloud.Server) (rules []hcloud.FirewallRule, firewalls int, err error) {
	for _, ref := range s.PublicNet.Firewalls {
		fw, _, err := c.Firewall.GetByID(ctx, ref.Firewall.ID)
		if err != nil {
			return nil, 0, err
		}
		if fw != nil {
			firewalls++
			rules = append(rules, InboundRules(fw)...)
		}
	}
	return rules, firewalls, nil
}

// Leftovers are closedvps firewalls with no server: from a run that stopped, or a server deleted in the Console.
func (c *Client) Leftovers(ctx context.Context) ([]*hcloud.Firewall, error) {
	fws, err := c.Firewall.AllWithOpts(ctx, hcloud.FirewallListOpts{ListOpts: hcloud.ListOpts{LabelSelector: ManagedLabel + "=" + ManagedValue}})
	if err != nil {
		return nil, err
	}
	var out []*hcloud.Firewall
	for _, fw := range fws {
		if len(fw.AppliedTo) == 0 && strings.HasPrefix(fw.Name, "closedvps-") {
			out = append(out, fw)
		}
	}
	return out, nil
}

// Snapshots are the snapshots made from a server, which Hetzner keeps, and bills, after the server goes.
func (c *Client) Snapshots(ctx context.Context, serverID int64) ([]*hcloud.Image, error) {
	imgs, err := c.Image.AllWithOpts(ctx, hcloud.ImageListOpts{Type: []hcloud.ImageType{hcloud.ImageTypeSnapshot}})
	if err != nil {
		return nil, err
	}
	var out []*hcloud.Image
	for _, i := range imgs {
		if i.CreatedFrom != nil && i.CreatedFrom.ID == serverID {
			out = append(out, i)
		}
	}
	return out, nil
}

// DeleteServer deletes a server and waits until it's gone.
func (c *Client) DeleteServer(ctx context.Context, s *hcloud.Server) error {
	res, _, err := c.Server.DeleteWithResult(ctx, s)
	if err != nil {
		return err
	}
	if res != nil && res.Action != nil {
		return c.Wait(ctx, res.Action)
	}
	return nil
}
