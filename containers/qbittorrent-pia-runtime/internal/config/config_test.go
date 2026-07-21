package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadDefaultsAndValidation(t *testing.T) {
	t.Setenv("PIA_USERNAME", "user-fixture")
	t.Setenv("PIA_PASSWORD", "password-fixture")
	t.Setenv("PIA_ALLOWED_SUBNETS", "10.42.0.0/16,fd00:42::/64")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CandidateMin != 3 || cfg.CandidateMax != 6 || cfg.HealthFailures != 4 || cfg.HealthInterval != 15*time.Second || cfg.PFHelperUID != 65532 {
		t.Fatalf("unexpected defaults: %#v", cfg)
	}
	if len(cfg.AllowedSubnets) != 2 {
		t.Fatalf("allowed subnets=%d", len(cfg.AllowedSubnets))
	}
}

func TestRejectsUnsafePFHelperUID(t *testing.T) {
	t.Setenv("PIA_USERNAME", "user-fixture")
	t.Setenv("PIA_PASSWORD", "password-fixture")
	t.Setenv("PIA_PF_HELPER_UID", "1000")
	if _, err := Load(); err == nil {
		t.Fatal("accepted PF helper UID matching the application UID")
	}
}

func TestRejectsUnsafeConfiguration(t *testing.T) {
	t.Setenv("PIA_USERNAME", "user-fixture")
	t.Setenv("PIA_PASSWORD", "password-fixture")
	t.Setenv("PIA_ALLOWED_SUBNETS", "0.0.0.0/0")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "unsafe allowed subnet") {
		t.Fatalf("error=%v", err)
	}
	t.Setenv("PIA_ALLOWED_SUBNETS", "10.0.0.0/8")
	t.Setenv("PIA_AUTH_RETRY", "1m")
	if _, err := Load(); err == nil {
		t.Fatal("expected authentication retry validation")
	}
	t.Setenv("PIA_AUTH_RETRY", "15m")
	t.Setenv("PIA_TOKEN_URL", "http://example.invalid/token")
	if _, err := Load(); err == nil {
		t.Fatal("accepted an insecure token URL")
	}
}

func TestFirewallLoadDoesNotRequireCredentials(t *testing.T) {
	t.Setenv("PIA_USERNAME", "")
	t.Setenv("PIA_PASSWORD", "")
	if _, err := LoadFirewall(); err != nil {
		t.Fatal(err)
	}
}
