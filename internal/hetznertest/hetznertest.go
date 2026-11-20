// Package hetznertest is a fake Hetzner Cloud API for tests: just the endpoints closedvps uses, answering in
// the shapes https://docs.hetzner.cloud/ documents, with stock that comes and goes.
package hetznertest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Type is a server type on sale in one location.
type Type struct {
	Name     string
	Location string  // nbg1 when empty
	Arch     string  // x86 when empty
	Price    string  // per month before VAT
	Memory   float64 // GB; 4 when 0
	// InStock says whether the type shows as available on the n-th look at the stock, counting from 1.
	InStock func(look int) bool
	// Left is how many creates succeed before it sells out; below 0 is no limit.
	Left int
}

type Cloud struct {
	t  testing.TB
	mu sync.Mutex

	Types     []*Type
	IPv4Price string // in every location
	// FailCreate, when it returns an error code, refuses the create with it.
	FailCreate func(name string) (code string)
	// FailActions makes these action ids end in an error; a server's create action has the server's id.
	FailActions map[int]bool

	Looks       int
	Servers     []map[string]any
	UserData    map[string]string // by server name
	Firewalls   []map[string]any
	Deleted     []string // names of deleted firewalls
	Gone        []string // names of deleted servers
	Images      []map[string]any
	BackupsOn   []string // names of servers backups were turned on for
	FailedPaths []string // requests nothing here answers
}

// New starts a fake and returns it with its URL.
func New(t testing.TB, types ...*Type) (*Cloud, string) {
	c := &Cloud{t: t, Types: types, IPv4Price: "0.50", UserData: map[string]string{}}
	srv := httptest.NewServer(c)
	t.Cleanup(srv.Close)
	return c, srv.URL
}

// Always is in stock from the first look.
func Always(int) bool { return true }

// From is in stock from the n-th look on.
func From(n int) func(int) bool { return func(look int) bool { return look >= n } }

var page = map[string]any{"pagination": map[string]any{"page": 1, "per_page": 50, "next_page": nil, "last_page": 1}}

var locations = []map[string]any{
	{"id": 1, "name": "fsn1", "city": "Falkenstein", "country": "DE", "network_zone": "eu-central"},
	{"id": 2, "name": "nbg1", "city": "Nuremberg", "country": "DE", "network_zone": "eu-central"},
	{"id": 3, "name": "hel1", "city": "Helsinki", "country": "FI", "network_zone": "eu-central"},
}

func location(name string) map[string]any {
	for _, l := range locations {
		if l["name"] == name {
			return l
		}
	}
	return nil
}

func (t *Type) loc() string {
	if t.Location == "" {
		return "nbg1"
	}
	return t.Location
}

func (c *Cloud) typeJSON(i int, t *Type, avail bool) map[string]any {
	arch := t.Arch
	if arch == "" {
		arch = "x86"
	}
	l := location(t.loc())
	mem := t.Memory
	if mem == 0 {
		mem = 4
	}
	return map[string]any{
		"id": i + 1, "name": t.Name, "cpu_type": "shared", "architecture": arch, "cores": 2, "memory": mem, "disk": 40,
		"prices":    []any{map[string]any{"location": t.loc(), "price_monthly": map[string]string{"net": t.Price, "gross": t.Price}}},
		"locations": []any{map[string]any{"id": l["id"], "name": t.loc(), "available": avail, "recommended": true, "deprecation": nil}},
	}
}

func (c *Cloud) findType(raw json.RawMessage) (int, *Type) {
	var name string
	var id int
	if json.Unmarshal(raw, &name) != nil {
		_ = json.Unmarshal(raw, &id)
	}
	for i, t := range c.Types {
		if t.Name == name || i+1 == id {
			return i, t
		}
	}
	return -1, nil
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func apiError(w http.ResponseWriter, status int, code, msg string) {
	reply(w, status, map[string]any{"error": map[string]any{"code": code, "message": msg, "details": nil}})
}

func action(id int, command string) map[string]any {
	return map[string]any{
		"id": id, "command": command, "status": "success", "progress": 100,
		"started": "2026-11-19T09:00:00Z", "finished": "2026-11-19T09:00:05Z", "resources": []any{}, "error": nil,
	}
}

func idFrom(path, prefix string) int {
	rest := strings.TrimPrefix(path, prefix)
	n, _ := strconv.Atoi(strings.SplitN(rest, "/", 2)[0])
	return n
}

func (c *Cloud) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	p, q := r.URL.Path, r.URL.Query()
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		// Not the client under test: something else on this machine probing the port.
		apiError(w, 401, "unauthorized", "no token")
		return
	}
	switch {
	case r.Method == http.MethodGet && p == "/server_types":
		c.Looks++
		var types []any
		for i, t := range c.Types {
			types = append(types, c.typeJSON(i, t, t.InStock(c.Looks)))
		}
		reply(w, 200, map[string]any{"server_types": types, "meta": page})
	case r.Method == http.MethodGet && p == "/pricing":
		// Real responses list ipv4 and ipv6 under both floating_ips and primary_ips. hcloud-go v2.53.0 sizes its
		// primary IP list by the floating one, so a fake without floating_ips makes it panic.
		ipPrices := func(price string) []any {
			var out []any
			for _, l := range locations {
				out = append(out, map[string]any{"location": l["name"], "price_monthly": map[string]string{"net": price, "gross": price}})
			}
			return out
		}
		ips := []any{map[string]any{"type": "ipv4", "prices": ipPrices(c.IPv4Price)}, map[string]any{"type": "ipv6", "prices": ipPrices("0.00")}}
		reply(w, 200, map[string]any{"pricing": map[string]any{"currency": "EUR", "vat_rate": "19.00", "floating_ips": ips, "primary_ips": ips}})
	case r.Method == http.MethodGet && p == "/locations":
		reply(w, 200, map[string]any{"locations": locations, "meta": page})

	case r.Method == http.MethodGet && p == "/servers":
		out := []any{}
		for _, s := range c.Servers {
			if n := q.Get("name"); (n != "" && n != s["name"]) || s["gone"] == true {
				continue
			}
			if sel := q.Get("label_selector"); sel != "" {
				k, v, _ := strings.Cut(sel, "=")
				if s["labels"].(map[string]string)[k] != v {
					continue
				}
			}
			out = append(out, s)
		}
		reply(w, 200, map[string]any{"servers": out, "meta": page})
	case r.Method == http.MethodPost && p == "/servers":
		c.createServer(w, r)
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/actions/enable_backup"):
		s := c.Servers[idFrom(p, "/servers/")-1]
		s["backup_window"] = "22-02"
		c.BackupsOn = append(c.BackupsOn, s["name"].(string))
		reply(w, 201, map[string]any{"action": action(500+len(c.BackupsOn), "enable_backup")})
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/servers/"):
		reply(w, 200, map[string]any{"server": c.Servers[idFrom(p, "/servers/")-1]})
	case r.Method == http.MethodDelete && strings.HasPrefix(p, "/servers/"):
		id := idFrom(p, "/servers/")
		s := c.Servers[id-1]
		s["gone"] = true
		c.Gone = append(c.Gone, s["name"].(string))
		for _, f := range c.Firewalls {
			var keep []any
			for _, a := range f["applied_to"].([]any) {
				if a.(map[string]any)["server"].(map[string]any)["id"] != id {
					keep = append(keep, a)
				}
			}
			if keep == nil {
				keep = []any{}
			}
			f["applied_to"] = keep
		}
		reply(w, 200, map[string]any{"action": action(900+id, "delete_server")})
	case r.Method == http.MethodGet && p == "/images":
		out := []any{}
		for _, i := range c.Images {
			out = append(out, i)
		}
		reply(w, 200, map[string]any{"images": out, "meta": page})

	case r.Method == http.MethodGet && p == "/actions":
		var acts []any
		for _, id := range q["id"] {
			n, _ := strconv.Atoi(id)
			a := action(n, "x")
			if c.FailActions[n] {
				a["status"] = "error"
				a["error"] = map[string]any{"code": "action_failed", "message": "the host failed"}
			}
			acts = append(acts, a)
		}
		reply(w, 200, map[string]any{"actions": acts, "meta": page})

	case r.Method == http.MethodGet && p == "/ssh_keys":
		reply(w, 200, map[string]any{"ssh_keys": []any{}, "meta": page})
	case r.Method == http.MethodPost && p == "/ssh_keys":
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		reply(w, 201, map[string]any{"ssh_key": map[string]any{"id": 7, "name": in["name"], "fingerprint": "aa", "public_key": in["public_key"], "labels": in["labels"]}})

	case r.Method == http.MethodGet && p == "/firewalls":
		out := []any{}
		for _, f := range c.Firewalls {
			if n := q.Get("name"); (n == "" || f["name"] == n) && f["gone"] != true {
				out = append(out, f)
			}
		}
		reply(w, 200, map[string]any{"firewalls": out, "meta": page})
	case r.Method == http.MethodPost && p == "/firewalls":
		var in struct {
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		fw := map[string]any{"id": 100 + len(c.Firewalls) + 1, "name": in.Name, "labels": in.Labels, "rules": []any{}, "applied_to": []any{}}
		c.Firewalls = append(c.Firewalls, fw)
		reply(w, 201, map[string]any{"firewall": fw, "actions": []any{}})
	case r.Method == http.MethodDelete && strings.HasPrefix(p, "/firewalls/"):
		fw := c.Firewalls[idFrom(p, "/firewalls/")-101]
		fw["gone"] = true
		c.Deleted = append(c.Deleted, fw["name"].(string))
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && strings.HasPrefix(p, "/firewalls/"):
		reply(w, 200, map[string]any{"firewall": c.Firewalls[idFrom(p, "/firewalls/")-101]})

	default:
		c.FailedPaths = append(c.FailedPaths, r.Method+" "+p)
		apiError(w, 404, "not_found", "no fake for "+r.Method+" "+p)
	}
}

func (c *Cloud) createServer(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name       string            `json:"name"`
		ServerType json.RawMessage   `json:"server_type"`
		Location   string            `json:"location"`
		UserData   string            `json:"user_data"`
		Labels     map[string]string `json:"labels"`
		Firewalls  []struct {
			Firewall int `json:"firewall"`
		} `json:"firewalls"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		apiError(w, 400, "json_error", err.Error())
		return
	}
	i, t := c.findType(in.ServerType)
	switch {
	case t == nil:
		apiError(w, 400, "invalid_input", "unknown server type")
		return
	case c.FailCreate != nil && c.FailCreate(in.Name) != "":
		apiError(w, 412, c.FailCreate(in.Name), "refused by the test")
		return
	case t.Left == 0:
		apiError(w, 412, "resource_unavailable", t.Name+" is sold out")
		return
	}
	for _, s := range c.Servers {
		if s["name"] == in.Name {
			apiError(w, 409, "uniqueness_error", "server name is already used")
			return
		}
	}
	t.Left--
	id := len(c.Servers) + 1
	var fws []any
	for _, f := range in.Firewalls {
		fws = append(fws, map[string]any{"id": f.Firewall, "status": "applied"})
		fw := c.Firewalls[f.Firewall-101]
		fw["applied_to"] = append(fw["applied_to"].([]any), map[string]any{"type": "server", "server": map[string]any{"id": id}})
	}
	c.UserData[in.Name] = in.UserData
	s := map[string]any{
		"id": id, "name": in.Name, "status": "initializing", "created": "2026-11-19T09:00:00Z", "labels": in.Labels,
		"server_type": c.typeJSON(i, t, true), "location": location(in.Location), "backup_window": nil,
		"public_net": map[string]any{
			"ipv4":      map[string]any{"id": id, "ip": fmt.Sprintf("203.0.113.%d", id), "blocked": false},
			"ipv6":      nil,
			"firewalls": fws,
		},
		"private_net": []any{}, "volumes": []any{}, "load_balancers": []any{},
	}
	c.Servers = append(c.Servers, s)
	act := action(id, "create_server")
	act["status"], act["progress"], act["finished"] = "running", 0, nil // the client then polls /actions
	reply(w, 201, map[string]any{"server": s, "action": act, "next_actions": []any{}, "root_password": nil})
}

// Lock runs f with the fake's state held still, for reading it from a test.
func (c *Cloud) Lock(f func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f()
}
