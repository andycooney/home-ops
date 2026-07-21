package supervisor

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andycooney/home-ops/containers/qbittorrent-pia-runtime/internal/api"
	"github.com/andycooney/home-ops/containers/qbittorrent-pia-runtime/internal/config"
	"github.com/andycooney/home-ops/containers/qbittorrent-pia-runtime/internal/firewall"
	"github.com/andycooney/home-ops/containers/qbittorrent-pia-runtime/internal/health"
	"github.com/andycooney/home-ops/containers/qbittorrent-pia-runtime/internal/session"
	"github.com/andycooney/home-ops/containers/qbittorrent-pia-runtime/internal/wireguard"
)

func TestEveryStateTransitionIsLiveAndNotReadyExceptHealthy(t *testing.T) {
	status := health.NewStatus()
	s := Supervisor{Status: status}
	states := []State{StateBootstrap, StateSelecting, StateRegistering, StateStarting, StateVerifying, StateHealthy, StateFailingOver, StateAuthenticationFailed, StateBackoff, StateShuttingDown}
	for _, state := range states {
		ready := state == StateHealthy
		s.transition(state, ready)
		if status.State() != string(state) || status.Ready() != ready || !status.Live(time.Minute) {
			t.Fatalf("state=%s ready=%v", status.State(), status.Ready())
		}
	}
}

func TestChildEnvironmentRedactsSecretsAndDisablesGluetunOwners(t *testing.T) {
	secrets := []string{"PIA_USERNAME=user-sensitive", "PIA_PASSWORD=password-sensitive", "UNRELATED_TOKEN=token-sensitive", "APP_PRIVATE_KEY=key-sensitive", "VPN_PORT_FORWARDING_PASSWORD=pf-sensitive", "UNRELATED_SECRET=secret-sensitive", "AWS_ACCESS_KEY_ID=access-sensitive", "OPENVPN_USER=openvpn-sensitive"}
	source := append(secrets, "PATH=/usr/bin", "HEALTH_RESTART_VPN=on", "FIREWALL_ENABLED_DISABLING_IT_SHOOTS_YOU_IN_YOUR_FOOT=on")
	env := ChildEnvironment(source, "/run/pia/current/wg0.conf")
	joined := strings.Join(env, "\n")
	for _, secret := range []string{"user-sensitive", "password-sensitive", "token-sensitive", "key-sensitive", "pf-sensitive", "secret-sensitive", "access-sensitive", "openvpn-sensitive"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("secret leaked: %s", secret)
		}
	}
	required := []string{"VPN_SERVICE_PROVIDER=custom", "VPN_TYPE=wireguard", "FIREWALL_ENABLED_DISABLING_IT_SHOOTS_YOU_IN_YOUR_FOOT=off", "HEALTH_RESTART_VPN=off", "PUBLICIP_ENABLED=off", "VERSION_INFORMATION=off", "WIREGUARD_CONF_SECRETFILE=/run/pia/current/wg0.conf"}
	for _, value := range required {
		if !strings.Contains(joined, value) {
			t.Fatalf("missing %s", value)
		}
	}
	if strings.Count(joined, "HEALTH_RESTART_VPN=") != 1 || strings.Count(joined, "FIREWALL_ENABLED_DISABLING_IT_SHOOTS_YOU_IN_YOUR_FOOT=") != 1 {
		t.Fatal("security overrides were duplicated")
	}
}

func TestBackoffJitterBounds(t *testing.T) {
	for i := int64(0); i < 100; i++ {
		got := jitter(time.Minute, time.Unix(0, i))
		if got < 48*time.Second || got > 72*time.Second {
			t.Fatalf("jitter=%s", got)
		}
	}
}

func TestExponentialBackoffSequence(t *testing.T) {
	current := 30 * time.Second
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute}
	for _, expected := range want {
		current = nextBackoff(current)
		if current != expected {
			t.Fatalf("backoff=%s want=%s", current, expected)
		}
	}
}

type fakeVerifier struct {
	mu     sync.Mutex
	errors []error
}

func (v *fakeVerifier) Verify(context.Context, netip.Addr) (health.Result, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.errors) == 0 {
		return health.Result{}, nil
	}
	err := v.errors[0]
	v.errors = v.errors[1:]
	return health.Result{}, err
}

type fakeFirewall struct{ states []firewall.State }

func (f *fakeFirewall) Init(context.Context) error {
	f.states = append(f.states, firewall.Bootstrap)
	return nil
}
func (f *fakeFirewall) Apply(_ context.Context, state firewall.State, _ firewall.Endpoint) error {
	f.states = append(f.states, state)
	return nil
}

type fakePublisher struct{ ready, invalidated, currentInvalidated, published int }

func (p *fakePublisher) PublishCurrent(_ session.Generation) (string, error) {
	p.published++
	return "/tmp/generation", nil
}
func (p *fakePublisher) PublishReady(string) error { p.ready++; return nil }
func (p *fakePublisher) InvalidateReady() error    { p.invalidated++; return nil }
func (p *fakePublisher) InvalidateCurrent() error  { p.currentInvalidated++; return nil }
func (p *fakePublisher) Remove(string) error       { return nil }

type bootstrapAPI struct {
	cancel    context.CancelFunc
	fetches   int
	probes    int
	tokens    int
	registers int
}

func (a *bootstrapAPI) FetchServerList(context.Context) (api.ServerList, error) {
	a.fetches++
	a.cancel()
	return api.ServerList{}, errors.New("stop after bootstrap")
}
func (a *bootstrapAPI) Probe(context.Context, api.Candidate) error {
	a.probes++
	return nil
}
func (a *bootstrapAPI) Token(context.Context, string, string) (string, error) {
	a.tokens++
	return "", errors.New("unexpected token request")
}
func (a *bootstrapAPI) Register(context.Context, api.Candidate, string, string) (wireguard.Registration, error) {
	a.registers++
	return wireguard.Registration{}, errors.New("unexpected registration")
}

type countingProcess struct{ starts int }

func (p *countingProcess) Start(string, []string) (Child, error) {
	p.starts++
	return nil, errors.New("unexpected child start")
}

func TestBootstrapPublicIPRetriesBeforeAnySessionActivity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &bootstrapAPI{cancel: cancel}
	publisher := &fakePublisher{}
	process := &countingProcess{}
	fw := &fakeFirewall{}
	status := health.NewStatus()
	attempts := 0
	var sleeps []time.Duration
	s := Supervisor{
		Config:    config.Config{PublicIPURL: "https://example.invalid/ip", ProbeTimeout: time.Second},
		API:       client,
		Firewall:  fw,
		Publisher: publisher,
		Verifier:  &fakeVerifier{},
		Process:   process,
		Status:    status,
		PublicIP: func(context.Context, string, time.Duration) (netip.Addr, error) {
			attempts++
			if len(fw.states) != 1 || fw.states[0] != firewall.Bootstrap {
				t.Fatalf("fail-closed firewall was not the only activity before bootstrap: %v", fw.states)
			}
			if client.fetches != 0 || client.probes != 0 || client.tokens != 0 || client.registers != 0 || publisher.published != 0 || process.starts != 0 {
				t.Fatal("API, generation, or tunnel activity occurred before bootstrap succeeded")
			}
			if attempts < 3 {
				return netip.Addr{}, errors.New("transient public-IP failure")
			}
			return netip.MustParseAddr("198.51.100.10"), nil
		},
		Now: func() time.Time { return time.Unix(0, 0) },
		Sleep: func(_ context.Context, delay time.Duration) error {
			if status.State() != string(StateBackoff) || status.Ready() || !status.Live(time.Minute) {
				t.Fatalf("bootstrap backoff state=%s ready=%v live=%v", status.State(), status.Ready(), status.Live(time.Minute))
			}
			sleeps = append(sleeps, delay)
			return nil
		},
	}
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if attempts != 3 || client.fetches != 1 {
		t.Fatalf("public-IP attempts=%d server-list fetches=%d", attempts, client.fetches)
	}
	if client.probes != 0 || client.tokens != 0 || client.registers != 0 || publisher.published != 0 || process.starts != 0 {
		t.Fatalf("unexpected session activity: probes=%d tokens=%d registrations=%d generations=%d starts=%d", client.probes, client.tokens, client.registers, publisher.published, process.starts)
	}
	if len(sleeps) != 2 || sleeps[0] != 24*time.Second || sleeps[1] != 48*time.Second {
		t.Fatalf("bootstrap backoff=%v", sleeps)
	}
	if s.Status.Ready() || s.Status.State() != string(StateShuttingDown) {
		t.Fatalf("state=%s ready=%v", s.Status.State(), s.Status.Ready())
	}
}

type fakeChild struct{ done chan error }

func (c *fakeChild) Done() <-chan error       { return c.done }
func (c *fakeChild) Stop(time.Duration) error { return nil }

func TestHealthThresholdRevokesBeforeFailover(t *testing.T) {
	verifier := &fakeVerifier{errors: []error{errors.New("one"), errors.New("two"), errors.New("three"), errors.New("four")}}
	fw := &fakeFirewall{}
	publisher := &fakePublisher{}
	status := health.NewStatus()
	status.Set(string(StateHealthy), true)
	s := Supervisor{Config: config.Config{HealthInterval: time.Second, HealthFailures: 4, SessionMaxAge: time.Hour}, Verifier: verifier, Firewall: fw, Publisher: publisher, Status: status, child: &fakeChild{done: make(chan error)}, current: "gen", Now: func() time.Time { return time.Unix(1, 0) }, Sleep: func(context.Context, time.Duration) error { return nil }}
	err := s.monitorHealthy(context.Background(), api.Candidate{RegionID: "ca"}, firewall.Endpoint{IP: netip.MustParseAddr("192.0.2.1"), Port: 1337}, netip.MustParseAddr("198.51.100.1"))
	if err == nil {
		t.Fatal("threshold did not trigger")
	}
	if publisher.invalidated != 1 || status.Ready() {
		t.Fatalf("ready was not revoked: invalidated=%d ready=%v", publisher.invalidated, status.Ready())
	}
	if len(fw.states) == 0 || fw.states[0] != firewall.Verifying {
		t.Fatalf("firewall states=%v", fw.states)
	}
}

func TestProactiveRotation(t *testing.T) {
	times := []time.Time{time.Unix(1, 0), time.Unix(1, 0).Add(21 * time.Hour)}
	index := 0
	s := Supervisor{Config: config.Config{SessionMaxAge: 20 * time.Hour}, Now: func() time.Time {
		value := times[index]
		if index < len(times)-1 {
			index++
		}
		return value
	}, child: &fakeChild{done: make(chan error)}}
	if err := s.monitorHealthy(context.Background(), api.Candidate{}, firewall.Endpoint{}, netip.Addr{}); err == nil || !strings.Contains(err.Error(), "proactive") {
		t.Fatalf("error=%v", err)
	}
}

func TestLogDoesNotContainRepresentativeSecrets(t *testing.T) {
	var output bytes.Buffer
	s := Supervisor{Logger: log.New(&output, "", 0), Status: health.NewStatus()}
	s.transition(StateAuthenticationFailed, false)
	s.log("authentication failed; credentials were not logged")
	for _, secret := range []string{"fixture-user-sensitive", "fixture-password-sensitive", "fixture-token-sensitive"} {
		if strings.Contains(output.String(), secret) {
			t.Fatalf("secret leaked: %s", secret)
		}
	}
}

func TestOSProcessGracefulStopAndKillFallback(t *testing.T) {
	graceful := script(t, "trap 'exit 0' TERM INT\nwhile :; do sleep 1; done\n")
	child, err := (OSProcess{}).Start(graceful, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(25 * time.Millisecond)
	if err := child.Stop(time.Second); err != nil {
		t.Fatalf("graceful stop: %v", err)
	}
	stubborn := script(t, "trap '' TERM INT\nwhile :; do sleep 1; done\n")
	child, err = (OSProcess{}).Start(stubborn, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(25 * time.Millisecond)
	started := time.Now()
	_ = child.Stop(50 * time.Millisecond)
	if time.Since(started) > time.Second {
		t.Fatal("SIGKILL fallback was not bounded")
	}
}

func script(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "child.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
