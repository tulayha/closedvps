// Package tsapitest is a fake Tailscale API for tests: the OAuth token, the policy and its tests, devices and
// auth keys, in the shapes https://tailscale.com/api documents.
package tsapitest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type Tailnet struct {
	mu sync.Mutex

	Refuse  bool             // turn the OAuth client down
	Tag     bool             // the policy defines tag:closedvps
	Open    bool             // the policy lets tag:closedvps reach the user's devices
	Devices []map[string]any // as the API lists them
	Keys    []string         // descriptions of the keys made, each followed by its tags
}

func New(t testing.TB) (*Tailnet, string) {
	tn := &Tailnet{Tag: true}
	srv := httptest.NewServer(tn)
	t.Cleanup(srv.Close)
	return tn, srv.URL
}

// Device is a device in the shape the API lists it.
func Device(name, created string, online bool, tags ...string) map[string]any {
	if tags == nil {
		tags = []string{}
	}
	return map[string]any{
		"name": name + ".tail1234.ts.net", "hostname": name, "addresses": []string{"100.64.0.7", "fd7a::7"},
		"tags": tags, "created": created, "connectedToControl": online,
	}
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (tn *Tailnet) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tn.mu.Lock()
	defer tn.mu.Unlock()
	if r.URL.Path == "/api/v2/oauth/token" {
		if tn.Refuse {
			reply(w, 401, map[string]any{"error": "invalid_client"})
			return
		}
		reply(w, 200, map[string]any{"access_token": "t", "token_type": "Bearer", "expires_in": 3600})
		return
	}
	if r.Header.Get("Authorization") != "Bearer t" {
		reply(w, 401, map[string]any{"message": "not authorized"})
		return
	}
	switch p := strings.TrimPrefix(r.URL.Path, "/api/v2/tailnet/-/"); {
	case r.Method == http.MethodGet && p == "acl":
		owners := map[string]any{}
		if tn.Tag {
			owners["tag:closedvps"] = []string{"autogroup:admin"}
		}
		reply(w, 200, map[string]any{"tagOwners": owners, "grants": []any{}})
	case r.Method == http.MethodPost && p == "acl/validate":
		if tn.Open {
			reply(w, 200, map[string]any{"message": "test(s) failed", "data": []any{map[string]any{"user": "tag:closedvps", "errors": []string{"100.64.0.1:22: want: Drop, got: Accept"}}}})
			return
		}
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodGet && p == "devices":
		devs := tn.Devices
		if devs == nil {
			devs = []map[string]any{}
		}
		reply(w, 200, map[string]any{"devices": devs})
	case r.Method == http.MethodPost && p == "keys":
		var in struct {
			Description  string
			Capabilities struct {
				Devices struct {
					Create struct {
						Tags []string
					}
				}
			}
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		tn.Keys = append(tn.Keys, in.Description+" "+strings.Join(in.Capabilities.Devices.Create.Tags, ","))
		reply(w, 200, map[string]any{"id": fmt.Sprint(len(tn.Keys)), "key": fmt.Sprintf("tskey-auth-made%d", len(tn.Keys))})
	default:
		reply(w, 404, map[string]any{"message": "no fake for " + r.Method + " " + r.URL.Path})
	}
}

// Lock runs f with the fake held, to read what it recorded.
func (tn *Tailnet) Lock(f func()) {
	tn.mu.Lock()
	defer tn.mu.Unlock()
	f()
}
