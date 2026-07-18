package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultServerListURL = "https://serverlist.piaservers.net/vpninfo/servers/v6"
	DefaultTokenURL      = "https://www.privateinternetaccess.com/api/client/v2/token"
	DefaultPublicIPURL   = "https://www.cloudflare.com/cdn-cgi/trace"
)

type Config struct {
	Username, Password string
	PreferredRegions   []string
	AllowedSubnets     []netip.Prefix
	ServerListURL      string
	TokenURL           string
	PublicIPURL        string
	CACertPath         string
	RuntimeDir         string
	ListenAddress      string
	GluetunEntrypoint  string
	Interface          string
	ApplicationUID     int
	ReaderGID          int
	ServicePort        uint16
	CandidateMin       int
	CandidateMax       int
	ProbeTimeout       time.Duration
	TunnelTimeout      time.Duration
	HealthInterval     time.Duration
	HealthFailures     int
	AuthRetry          time.Duration
	SessionMaxAge      time.Duration
	ShutdownGrace      time.Duration
}

func Load() (Config, error) { return load(true) }

func LoadFirewall() (Config, error) { return load(false) }

func load(requireCredentials bool) (Config, error) {
	c := Config{
		Username:          first("PIA_USERNAME", "VPN_PORT_FORWARDING_USERNAME"),
		Password:          first("PIA_PASSWORD", "VPN_PORT_FORWARDING_PASSWORD"),
		PreferredRegions:  csv(os.Getenv("PIA_PREFERRED_REGIONS")),
		ServerListURL:     env("PIA_SERVER_LIST_URL", DefaultServerListURL),
		TokenURL:          env("PIA_TOKEN_URL", DefaultTokenURL),
		PublicIPURL:       env("PIA_PUBLIC_IP_URL", DefaultPublicIPURL),
		CACertPath:        env("PIA_CA_CERT", "/usr/local/share/pia/ca.rsa.4096.crt"),
		RuntimeDir:        env("PIA_RUNTIME_DIR", "/run/pia"),
		ListenAddress:     env("PIA_RUNTIME_LISTEN", "0.0.0.0:8001"),
		GluetunEntrypoint: env("GLUETUN_ENTRYPOINT", "/gluetun-entrypoint"),
		Interface:         env("PIA_TUNNEL_INTERFACE", "tun0"),
	}
	var err error
	if c.AllowedSubnets, err = prefixes(csv(os.Getenv("PIA_ALLOWED_SUBNETS"))); err != nil {
		return Config{}, err
	}
	if c.ApplicationUID, err = integer("PIA_APPLICATION_UID", 1000); err != nil {
		return Config{}, err
	}
	if c.ReaderGID, err = integer("PIA_READER_GID", 65532); err != nil {
		return Config{}, err
	}
	port, err := integer("PIA_SERVICE_PORT", 8080)
	if err != nil || port < 1 || port > 65535 {
		return Config{}, fmt.Errorf("PIA_SERVICE_PORT must be 1..65535")
	}
	c.ServicePort = uint16(port)
	if c.CandidateMin, err = integer("PIA_CANDIDATE_MIN", 3); err != nil {
		return Config{}, err
	}
	if c.CandidateMax, err = integer("PIA_CANDIDATE_MAX", 6); err != nil {
		return Config{}, err
	}
	for key, target := range map[string]struct {
		p   *time.Duration
		def time.Duration
	}{
		"PIA_PROBE_TIMEOUT":   {&c.ProbeTimeout, 5 * time.Second},
		"PIA_TUNNEL_TIMEOUT":  {&c.TunnelTimeout, 120 * time.Second},
		"PIA_HEALTH_INTERVAL": {&c.HealthInterval, 15 * time.Second},
		"PIA_AUTH_RETRY":      {&c.AuthRetry, 15 * time.Minute},
		"PIA_SESSION_MAX_AGE": {&c.SessionMaxAge, 20 * time.Hour},
		"PIA_SHUTDOWN_GRACE":  {&c.ShutdownGrace, 10 * time.Second},
	} {
		*target.p, err = duration(key, target.def)
		if err != nil {
			return Config{}, err
		}
	}
	if c.HealthFailures, err = integer("PIA_HEALTH_FAILURES", 4); err != nil {
		return Config{}, err
	}
	if requireCredentials {
		return c, c.Validate()
	}
	return c, c.validateFirewall()
}

func (c Config) Validate() error {
	if c.Username == "" || c.Password == "" {
		return errors.New("PIA credentials are required")
	}
	if err := c.validateFirewall(); err != nil {
		return err
	}
	if c.CandidateMin < 3 || c.CandidateMax < c.CandidateMin || c.CandidateMax > 32 {
		return errors.New("candidate bounds must satisfy 3 <= min <= max <= 32")
	}
	if c.HealthFailures < 1 || c.HealthFailures > 20 {
		return errors.New("health failure threshold must be 1..20")
	}
	if c.ProbeTimeout <= 0 || c.TunnelTimeout <= 0 || c.HealthInterval <= 0 || c.AuthRetry < 15*time.Minute || c.SessionMaxAge <= 0 || c.ShutdownGrace <= 0 {
		return errors.New("durations must be positive and authentication retry must be at least 15m")
	}
	for _, endpoint := range []string{c.ServerListURL, c.TokenURL, c.PublicIPURL} {
		if err := validateHTTPS(endpoint); err != nil {
			return err
		}
	}
	if !filepath.IsAbs(c.CACertPath) || !filepath.IsAbs(c.RuntimeDir) || filepath.Clean(c.RuntimeDir) == "/" || !filepath.IsAbs(c.GluetunEntrypoint) {
		return errors.New("CA, runtime, and Gluetun paths must be safe absolute paths")
	}
	if _, _, err := net.SplitHostPort(c.ListenAddress); err != nil {
		return errors.New("probe listen address must include a valid port")
	}
	return nil
}

func (c Config) validateFirewall() error {
	if c.ApplicationUID <= 0 || c.ReaderGID <= 0 || c.ServicePort == 0 || c.Interface == "" {
		return errors.New("invalid firewall identity, port, or interface")
	}
	for _, p := range c.AllowedSubnets {
		if !p.IsValid() || p.Addr().IsUnspecified() || p.Bits() == 0 {
			return fmt.Errorf("unsafe allowed subnet %q", p)
		}
	}
	return nil
}

func first(keys ...string) string {
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return ""
}
func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
func csv(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}
func prefixes(values []string) ([]netip.Prefix, error) {
	out := make([]netip.Prefix, 0, len(values))
	for _, value := range values {
		p, err := netip.ParsePrefix(value)
		if err != nil {
			return nil, fmt.Errorf("invalid subnet: %w", err)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}
func integer(key string, def int) (int, error) {
	value := os.Getenv(key)
	if value == "" {
		return def, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	return n, nil
}
func duration(key string, def time.Duration) (time.Duration, error) {
	value := os.Getenv(key)
	if value == "" {
		return def, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration", key)
	}
	return d, nil
}

func validateHTTPS(value string) error {
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return errors.New("PIA and verification endpoints must be credential-free HTTPS URLs")
	}
	return nil
}
