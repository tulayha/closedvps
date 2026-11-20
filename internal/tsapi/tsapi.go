// Package tsapi is what closedvps needs from the Tailscale API, through Tailscale's official client: one-off
// tagged keys, a look at the policy before buying, and the device list for seeing a server join.
package tsapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"golang.org/x/oauth2"
	tailscale "tailscale.com/client/tailscale/v2"
)

// Tag is the tag every closedvps server carries, so the policy can keep it away from the user's devices.
const Tag = "tag:closedvps"

// Scopes are what the OAuth client needs: making keys, reading the policy (Tailscale also asks for device
// reads with it), and reading devices.
var Scopes = []string{"auth_keys", "policy_file:read", "devices:core:read"}

type Client struct{ c *tailscale.Client }

// New makes a client from an OAuth client's ID and secret. baseURL is empty for the real API.
func New(clientID, secret, baseURL, userAgent string) (*Client, error) {
	c := &tailscale.Client{
		UserAgent: userAgent,
		Auth:      &tailscale.OAuth{ClientID: clientID, ClientSecret: secret, Scopes: Scopes},
	}
	if baseURL != "" {
		u, err := url.Parse(baseURL)
		if err != nil {
			return nil, err
		}
		c.BaseURL = u
	}
	return &Client{c}, nil
}

// NewKey makes a one-off, pre-approved auth key tagged Tag, valid for ttl.
func (c *Client) NewKey(ctx context.Context, server string, ttl time.Duration) (string, error) {
	var req tailscale.CreateKeyRequest
	req.Capabilities.Devices.Create.Preauthorized = true
	req.Capabilities.Devices.Create.Tags = []string{Tag}
	req.ExpirySeconds = int64(ttl.Seconds())
	req.Description = "closedvps " + server
	k, err := c.c.Keys().CreateAuthKey(ctx, req)
	if err != nil {
		return "", err
	}
	return k.Key, nil
}

// Policy is what the policy means for a closedvps server.
type Policy int

const (
	PolicyOK    Policy = iota // the tag exists and can't reach the user's devices
	PolicyNoTag               // the tag isn't defined, so no key can carry it
	PolicyOpen                // the tag exists, but a rule lets it reach the user's devices
)

// CheckPolicy reads the policy and, when the tag exists, has Tailscale run a test of its own: from the tag,
// SSH to each device a person owns must be denied. The test runs against the stored policy and changes nothing.
func (c *Client) CheckPolicy(ctx context.Context) (Policy, error) {
	acl, err := c.c.PolicyFile().Get(ctx)
	if err != nil {
		return 0, err
	}
	if _, ok := acl.TagOwners[Tag]; !ok {
		return PolicyNoTag, nil
	}
	devices, err := c.c.Devices().List(ctx)
	if err != nil {
		return 0, err
	}
	var deny []string
	for _, d := range devices {
		if len(d.Tags) == 0 && len(d.Addresses) > 0 && !strings.Contains(d.Addresses[0], ":") {
			deny = append(deny, d.Addresses[0]+":22")
		}
	}
	if len(deny) == 0 {
		return PolicyOK, nil
	}
	// The client reports failed tests (a 200 with a message) as an error of its own making, and anything
	// else, like a refused token, as an APIError.
	switch err := c.c.PolicyFile().Validate(ctx, []tailscale.ACLTest{{Source: Tag, Deny: deny}}); {
	case err == nil:
		return PolicyOK, nil
	case errors.As(err, new(tailscale.APIError)):
		return 0, err
	case strings.HasPrefix(err.Error(), "ACL validation failed"):
		return PolicyOpen, nil
	default:
		return 0, err
	}
}

// Device is a machine on the tailnet.
type Device struct {
	Name    string // the short MagicDNS name
	Online  bool
	Tagged  bool
	Created time.Time
}

// Devices lists the tailnet's machines.
func (c *Client) Devices(ctx context.Context) ([]Device, error) {
	ds, err := c.c.Devices().List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Device, 0, len(ds))
	for _, d := range ds {
		name, _, _ := strings.Cut(d.Name, ".")
		if name == "" {
			name = d.Hostname
		}
		out = append(out, Device{Name: name, Online: d.ConnectedToControl, Tagged: slices.Contains(d.Tags, Tag), Created: d.Created.Time})
	}
	return out, nil
}

// Refused reports whether Tailscale turned the credentials down, as opposed to not answering.
func Refused(err error) bool {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		return true
	}
	var apiErr tailscale.APIError
	return errors.As(err, &apiErr) && (apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden)
}

// Describe turns an API error into a short sentence.
func Describe(err error) string {
	var apiErr tailscale.APIError
	if errors.As(err, &apiErr) && apiErr.Message != "" {
		return apiErr.Message
	}
	return fmt.Sprint(err)
}
