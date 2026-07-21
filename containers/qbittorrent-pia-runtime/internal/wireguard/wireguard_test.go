package wireguard

import (
	"strings"
	"testing"
)

func TestEphemeralKeysAndConfig(t *testing.T) {
	one, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	two, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if one.Private == two.Private || one.Public == two.Public {
		t.Fatal("ephemeral keys were reused")
	}
	registration := Registration{PeerIP: "10.0.0.2/32", ServerKey: two.Public, ServerIP: "10.0.0.1", ServerPort: 1337, DNSServers: []string{"10.0.0.1"}}
	conf, err := BuildConfig(one, "192.0.2.1", registration)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateConfig(conf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(conf, "AllowedIPs = 0.0.0.0/0") {
		t.Fatal("full tunnel is missing")
	}
}

func TestConfigRejectsIncompleteRegistration(t *testing.T) {
	keys, _ := GenerateKeyPair()
	_, err := BuildConfig(keys, "not-an-ip", Registration{})
	if err == nil {
		t.Fatal("expected validation error")
	}
}
