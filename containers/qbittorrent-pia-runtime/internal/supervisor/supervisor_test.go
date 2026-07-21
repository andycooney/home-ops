package supervisor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
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

type fakeFirewall struct {
	states      []firewall.State
	endpoints   []firewall.Endpoint
	initErr     error
	applyErrors map[int]error
	applyCalls  int
	events      *[]string
}

func (f *fakeFirewall) Init(context.Context) error {
	f.states = append(f.states, firewall.Bootstrap)
	return f.initErr
}
func (f *fakeFirewall) Apply(_ context.Context, state firewall.State, endpoint firewall.Endpoint) error {
	f.states = append(f.states, state)
	f.endpoints = append(f.endpoints, endpoint)
	if f.events != nil {
		*f.events = append(*f.events, "firewall:"+string(state))
	}
	err := f.applyErrors[f.applyCalls]
	f.applyCalls++
	return err
}

type fakePublisher struct {
	ready, invalidated, currentInvalidated, published int
	pfPort                                            uint16
	pfErr                                             error
	publishCurrentErr                                 error
	publishReadyErr                                   error
	invalidateReadyErr                                error
	invalidateCurrentErr                              error
	removeErr                                         error
	removed                                           int
	events                                            *[]string
	lastGeneration                                    session.Generation
}

func (p *fakePublisher) PublishCurrent(generation session.Generation) (string, error) {
	p.published++
	p.lastGeneration = generation
	return "/tmp/generation", p.publishCurrentErr
}
func (p *fakePublisher) PublishReady(string) error {
	p.ready++
	if p.events != nil {
		*p.events = append(*p.events, "publish-ready")
	}
	return p.publishReadyErr
}
func (p *fakePublisher) ReadForwardedPort(string) (uint16, error) {
	if p.pfPort == 0 && p.pfErr == nil {
		return 0, session.ErrPFPortPending
	}
	return p.pfPort, p.pfErr
}
func (p *fakePublisher) InvalidateReady() error {
	p.invalidated++
	if p.events != nil {
		*p.events = append(*p.events, "invalidate-ready")
	}
	return p.invalidateReadyErr
}
func (p *fakePublisher) InvalidateCurrent() error {
	p.currentInvalidated++
	if p.events != nil {
		*p.events = append(*p.events, "invalidate-current")
	}
	return p.invalidateCurrentErr
}
func (p *fakePublisher) Remove(string) error {
	p.removed++
	if p.events != nil {
		*p.events = append(*p.events, "remove")
	}
	return p.removeErr
}

type bootstrapAPI struct {
	cancel    context.CancelFunc
	fetches   int
	probes    int
	tokens    int
	registers int
}

type cycleAPI struct{}

type candidateListAPI struct{ count int }

func (a candidateListAPI) FetchServerList(context.Context) (api.ServerList, error) {
	pf, offline := true, false
	endpoints := make([]api.Endpoint, 0, a.count)
	for index := 1; index <= a.count; index++ {
		endpoints = append(endpoints, api.Endpoint{IP: fmt.Sprintf("192.0.2.%d", index), Hostname: fmt.Sprintf("ca-%d.example.invalid", index)})
	}
	return api.ServerList{
		Groups:  map[string][]api.Group{"wg": {{Name: "wireguard"}}},
		Regions: []api.Region{{ID: "ca", Name: "Canada", Country: "CA", PortForward: &pf, Offline: &offline, Servers: api.Servers{WG: endpoints}}},
	}, nil
}
func (candidateListAPI) Probe(context.Context, api.Candidate) error {
	return errors.New("unexpected probe outside candidate attempt")
}
func (candidateListAPI) Token(context.Context, string, string) (string, error) {
	return "", errors.New("unexpected token outside candidate attempt")
}
func (candidateListAPI) Register(context.Context, api.Candidate, string, string) (wireguard.Registration, error) {
	return wireguard.Registration{}, errors.New("unexpected registration outside candidate attempt")
}

func (cycleAPI) FetchServerList(context.Context) (api.ServerList, error) {
	pf, offline := true, false
	return api.ServerList{
		Groups:  map[string][]api.Group{"wg": {{Name: "wireguard"}}},
		Regions: []api.Region{{ID: "ca", Name: "Canada", Country: "CA", PortForward: &pf, Offline: &offline, Servers: api.Servers{WG: []api.Endpoint{{IP: "192.0.2.10", Hostname: "ca.example.invalid"}}}}},
	}, nil
}
func (cycleAPI) Probe(context.Context, api.Candidate) error { return nil }
func (cycleAPI) Token(context.Context, string, string) (string, error) {
	return "token-fixture", nil
}
func (cycleAPI) Register(context.Context, api.Candidate, string, string) (wireguard.Registration, error) {
	keys, err := wireguard.GenerateKeyPair()
	if err != nil {
		return wireguard.Registration{}, err
	}
	return wireguard.Registration{PeerIP: "10.0.0.2/32", ServerKey: keys.Public, ServerIP: "10.0.0.1", ServerPort: 1337, DNSServers: []string{"10.0.0.1"}}, nil
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
			for _, state := range fw.states {
				if state != firewall.Bootstrap {
					t.Fatalf("fail-closed bootstrap firewall was not the only activity before bootstrap: %v", fw.states)
				}
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

func TestBootstrapFirewallFailureLocksWithoutPublicIPActivity(t *testing.T) {
	fw := &fakeFirewall{applyErrors: map[int]error{0: errors.New("bootstrap firewall failed")}}
	publicIPCalls := 0
	s := Supervisor{
		Config:   config.Config{ShutdownGrace: time.Second},
		Firewall: fw,
		Status:   health.NewStatus(),
		PublicIP: func(context.Context, string, time.Duration) (netip.Addr, error) {
			publicIPCalls++
			return netip.MustParseAddr("198.51.100.1"), nil
		},
	}
	if _, err := s.bootstrapPublicIP(context.Background()); err == nil || !strings.Contains(err.Error(), "bootstrap firewall failed") {
		t.Fatalf("error=%v", err)
	}
	if strings.Join(firewallStates(fw.states), ",") != "BOOTSTRAP,LOCKED" || publicIPCalls != 0 || s.Status.Ready() {
		t.Fatalf("states=%v public-IP calls=%d ready=%v", fw.states, publicIPCalls, s.Status.Ready())
	}
}

func TestNewSessionCycleRefreshesPreTunnelPublicIPAfterCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fw := &fakeFirewall{}
	publisher := &fakePublisher{}
	process := &countingProcess{}
	child := &fakeChild{done: make(chan error)}
	publicIPs := []netip.Addr{netip.MustParseAddr("198.51.100.10"), netip.MustParseAddr("198.51.100.11")}
	publicIPCalls := 0
	var compared []netip.Addr
	s := Supervisor{
		Config:    config.Config{PublicIPURL: "https://example.invalid/ip", ProbeTimeout: time.Second, ShutdownGrace: time.Second},
		API:       &bootstrapAPI{cancel: cancel},
		Firewall:  fw,
		Publisher: publisher,
		Verifier:  &fakeVerifier{},
		Process:   process,
		Status:    health.NewStatus(),
		PublicIP: func(context.Context, string, time.Duration) (netip.Addr, error) {
			if publicIPCalls >= 1 && (child.stops != 1 || publisher.removed != 1 || len(compared) != 1) {
				t.Fatalf("new bootstrap began before prior generation cleanup: stops=%d removals=%d", child.stops, publisher.removed)
			}
			publicIPCalls++
			if publicIPCalls == 2 {
				return netip.Addr{}, errors.New("transient refreshed public-IP failure")
			}
			if publicIPCalls == 1 {
				return publicIPs[0], nil
			}
			return publicIPs[1], nil
		},
		Now:   func() time.Time { return time.Unix(0, 0) },
		Sleep: func(context.Context, time.Duration) error { return nil },
	}
	s.cycle = func(_ context.Context, preTunnelIP netip.Addr) error {
		compared = append(compared, preTunnelIP)
		if len(compared) == 1 {
			s.child = child
			s.current = "gen-one"
			return errors.New("rotate session")
		}
		cancel()
		return ctx.Err()
	}
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if publicIPCalls != 3 || len(compared) != 2 || compared[0] != publicIPs[0] || compared[1] != publicIPs[1] {
		t.Fatalf("public-IP calls=%d comparison values=%v", publicIPCalls, compared)
	}
	if strings.Join(firewallStates(fw.states[:4]), ",") != "BOOTSTRAP,BOOTSTRAP,LOCKED,BOOTSTRAP" {
		t.Fatalf("firewall sequence=%v", fw.states)
	}
}

func TestCandidateMinimumMaximumCleanupFreshBootstrapAndOuterBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	now := time.Unix(100, 0)
	fw := &fakeFirewall{}
	publisher := &fakePublisher{}
	status := health.NewStatus()
	var attempts []string
	var comparisonIPs []netip.Addr
	publicIPCalls := 0
	outerBackoffs := 0
	var s Supervisor
	s = Supervisor{
		Config: config.Config{
			CandidateMin:  3,
			CandidateMax:  4,
			PublicIPURL:   "https://example.invalid/ip",
			ProbeTimeout:  time.Second,
			ShutdownGrace: time.Second,
		},
		API:       candidateListAPI{count: 5},
		Firewall:  fw,
		Publisher: publisher,
		Verifier:  &fakeVerifier{},
		Process:   &countingProcess{},
		Status:    status,
		PublicIP: func(context.Context, string, time.Duration) (netip.Addr, error) {
			if s.child != nil || s.current != "" || publisher.removed != len(attempts) {
				t.Fatalf("fresh bootstrap preceded cleanup: child=%v current=%q removals=%d attempts=%d", s.child, s.current, publisher.removed, len(attempts))
			}
			publicIPCalls++
			return netip.MustParseAddr(fmt.Sprintf("198.51.100.%d", publicIPCalls)), nil
		},
		Now: func() time.Time { return now },
		Sleep: func(ctx context.Context, _ time.Duration) error {
			outerBackoffs++
			if len(attempts) != 4 {
				t.Fatalf("outer backoff began after %d attempts", len(attempts))
			}
			cancel()
			return ctx.Err()
		},
		cooldown: map[string]time.Time{"192.0.2.2": now.Add(time.Hour)},
	}
	s.attempt = func(_ context.Context, candidate api.Candidate, comparisonIP netip.Addr) error {
		if s.child != nil || s.current != "" || publisher.removed != len(attempts) {
			t.Fatalf("parallel or unclean candidate start for %s: child=%v current=%q removals=%d attempts=%d", candidate.IP, s.child, s.current, publisher.removed, len(attempts))
		}
		for _, attempted := range attempts {
			if attempted == candidate.IP {
				t.Fatalf("candidate %s attempted twice", candidate.IP)
			}
		}
		attempts = append(attempts, candidate.IP)
		comparisonIPs = append(comparisonIPs, comparisonIP)
		s.child = &fakeChild{done: make(chan error)}
		s.current = fmt.Sprintf("gen-%d", len(attempts))
		return errors.New("deterministic candidate failure")
	}
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if strings.Join(attempts, ",") != "192.0.2.1,192.0.2.3,192.0.2.4,192.0.2.5" {
		t.Fatalf("attempts=%v", attempts)
	}
	if len(attempts) < s.Config.CandidateMin || len(attempts) > s.Config.CandidateMax || publicIPCalls != len(attempts) {
		t.Fatalf("attempts=%d min=%d max=%d public-IP calls=%d", len(attempts), s.Config.CandidateMin, s.Config.CandidateMax, publicIPCalls)
	}
	for index, comparisonIP := range comparisonIPs {
		want := netip.MustParseAddr(fmt.Sprintf("198.51.100.%d", index+1))
		if comparisonIP != want {
			t.Fatalf("comparison IP %d=%s want=%s", index, comparisonIP, want)
		}
	}
	if publisher.removed != len(attempts) || s.child != nil || s.current != "" || outerBackoffs != 1 {
		t.Fatalf("removals=%d child=%v current=%q outer backoffs=%d", publisher.removed, s.child, s.current, outerBackoffs)
	}
	for _, endpoint := range attempts {
		if until, found := s.cooldown[endpoint]; !found || !until.After(now) {
			t.Fatalf("failed endpoint %s was not cooled", endpoint)
		}
	}
}

func TestCyclePublicationAndFirewallFailuresPropagate(t *testing.T) {
	newSupervisor := func(fw *fakeFirewall, publisher *fakePublisher) *Supervisor {
		return &Supervisor{
			Config:    config.Config{CandidateMax: 1},
			API:       cycleAPI{},
			Firewall:  fw,
			Publisher: publisher,
			Verifier:  &fakeVerifier{},
			Process:   &countingProcess{},
			Status:    health.NewStatus(),
			Now:       func() time.Time { return time.Unix(1, 0) },
			cooldown:  map[string]time.Time{},
		}
	}
	t.Run("current generation publication", func(t *testing.T) {
		publisher := &fakePublisher{publishCurrentErr: errors.New("publish current failed")}
		err := newSupervisor(&fakeFirewall{}, publisher).runCycle(context.Background(), netip.MustParseAddr("198.51.100.1"))
		if err == nil || !strings.Contains(err.Error(), "publish current failed") || publisher.published != 1 || publisher.lastGeneration.PFGateway != "10.0.0.1" {
			t.Fatalf("error=%v publications=%d PF gateway=%q", err, publisher.published, publisher.lastGeneration.PFGateway)
		}
	})
	for _, tc := range []struct {
		name      string
		failCall  int
		wantState firewall.State
	}{
		{name: "selection bootstrap", failCall: 0, wantState: firewall.Bootstrap},
		{name: "candidate bootstrap", failCall: 1, wantState: firewall.Bootstrap},
		{name: "registration endpoint", failCall: 2, wantState: firewall.Selected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fw := &fakeFirewall{applyErrors: map[int]error{tc.failCall: errors.New("firewall failed")}}
			publisher := &fakePublisher{}
			err := newSupervisor(fw, publisher).runCycle(context.Background(), netip.MustParseAddr("198.51.100.1"))
			if err == nil || !strings.Contains(err.Error(), "firewall failed") || fw.states[tc.failCall] != tc.wantState {
				t.Fatalf("error=%v states=%v", err, fw.states)
			}
		})
	}
}

func TestInitialVerificationFirewallFailureLocksBeforeChildStart(t *testing.T) {
	fw := &fakeFirewall{applyErrors: map[int]error{0: errors.New("verifying failed")}}
	process := &countingProcess{}
	s := Supervisor{
		Config:    config.Config{ShutdownGrace: time.Second},
		Firewall:  fw,
		Publisher: &fakePublisher{},
		Verifier:  &fakeVerifier{},
		Process:   process,
		Status:    health.NewStatus(),
	}
	err := s.startAndVerify(context.Background(), "/run/pia/sessions/gen-one", "10.0.0.1", activeEndpoint(), netip.MustParseAddr("198.51.100.1"))
	if err == nil || !strings.Contains(err.Error(), "verifying failed") {
		t.Fatalf("error=%v", err)
	}
	if strings.Join(firewallStates(fw.states), ",") != "VERIFYING,LOCKED" || process.starts != 0 {
		t.Fatalf("states=%v child starts=%d", fw.states, process.starts)
	}
}

func TestFirewallInitFailureMarksSupervisorNotLive(t *testing.T) {
	status := health.NewStatus()
	s := Supervisor{
		Config:    config.Config{},
		API:       &bootstrapAPI{},
		Firewall:  &fakeFirewall{initErr: errors.New("init failed")},
		Publisher: &fakePublisher{},
		Verifier:  &fakeVerifier{},
		Process:   &countingProcess{},
		Status:    status,
	}
	if err := s.Run(context.Background()); err == nil || status.Live(time.Minute) || status.Ready() {
		t.Fatalf("error=%v live=%v ready=%v", err, status.Live(time.Minute), status.Ready())
	}
}

type fakeChild struct {
	done       chan error
	stops      int
	stopErr    error
	stopErrors []error
	events     *[]string
}

func (c *fakeChild) Done() <-chan error { return c.done }
func (c *fakeChild) Stop(time.Duration) error {
	c.stops++
	if c.events != nil {
		*c.events = append(*c.events, "stop-child")
	}
	if len(c.stopErrors) != 0 {
		err := c.stopErrors[0]
		c.stopErrors = c.stopErrors[1:]
		return err
	}
	return c.stopErr
}

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

func activeEndpoint() firewall.Endpoint {
	return firewall.Endpoint{IP: netip.MustParseAddr("192.0.2.10"), Port: 1337, PFGateway: netip.MustParseAddr("192.0.2.20")}
}

func TestPFPortSynchronizationAndHealthFailureRemoval(t *testing.T) {
	fw := &fakeFirewall{}
	publisher := &fakePublisher{pfPort: 49152}
	status := health.NewStatus()
	status.Set(string(StateHealthy), true)
	s := Supervisor{Firewall: fw, Publisher: publisher, Status: status, current: "gen-one"}
	if err := s.syncForwardedPort(context.Background(), activeEndpoint()); err != nil {
		t.Fatal(err)
	}
	if s.pfPort != 49152 || len(fw.endpoints) != 1 || fw.endpoints[0].ForwardedPort != 49152 || fw.states[0] != firewall.Healthy {
		t.Fatalf("PF port=%d states=%v endpoints=%+v", s.pfPort, fw.states, fw.endpoints)
	}
	if err := s.restrictForVerification(context.Background(), activeEndpoint()); err != nil {
		t.Fatal(err)
	}
	if s.pfPort != 0 || status.Ready() || fw.states[len(fw.states)-1] != firewall.Verifying || fw.endpoints[len(fw.endpoints)-1].ForwardedPort != 0 || publisher.invalidated != 1 {
		t.Fatalf("PF allowance was not removed: port=%d ready=%v states=%v endpoints=%+v invalidated=%d", s.pfPort, status.Ready(), fw.states, fw.endpoints, publisher.invalidated)
	}

	s.pfPort = 49152
	publisher.pfPort = 0
	publisher.pfErr = session.ErrPFPortPending
	if err := s.syncForwardedPort(context.Background(), activeEndpoint()); err != nil || s.pfPort != 0 || fw.endpoints[len(fw.endpoints)-1].ForwardedPort != 0 {
		t.Fatalf("unpublished PF port error=%v port=%d", err, s.pfPort)
	}

	s.pfPort = 49152
	publisher.pfErr = session.ErrPFPortStale
	if err := s.syncForwardedPort(context.Background(), activeEndpoint()); !errors.Is(err, session.ErrPFPortStale) {
		t.Fatalf("stale generation error=%v", err)
	}
	if s.pfPort != 0 || fw.endpoints[len(fw.endpoints)-1].ForwardedPort != 0 {
		t.Fatal("stale PF data did not revoke the inbound allowance")
	}
}

func TestPFPortFirewallFailuresLockAndStop(t *testing.T) {
	for _, tc := range []struct {
		name       string
		activePort uint16
		published  uint16
		publishErr error
	}{
		{name: "install", published: 49152},
		{name: "revoke invalid", activePort: 49152, publishErr: session.ErrPFPortStale},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fw := &fakeFirewall{applyErrors: map[int]error{0: errors.New("PF firewall failed")}}
			publisher := &fakePublisher{pfPort: tc.published, pfErr: tc.publishErr}
			child := &fakeChild{done: make(chan error)}
			s := Supervisor{
				Config:    config.Config{ShutdownGrace: time.Second},
				Firewall:  fw,
				Publisher: publisher,
				Status:    health.NewStatus(),
				child:     child,
				current:   "gen-one",
				pfPort:    tc.activePort,
			}
			if err := s.syncForwardedPort(context.Background(), activeEndpoint()); err == nil || !strings.Contains(err.Error(), "PF firewall failed") {
				t.Fatalf("error=%v", err)
			}
			if strings.Join(firewallStates(fw.states), ",") != "HEALTHY,LOCKED" || child.stops != 1 || s.pfPort != 0 {
				t.Fatalf("states=%v stops=%d PF port=%d", fw.states, child.stops, s.pfPort)
			}
		})
	}
}

func TestReadyPromotionFirewallBeforeMetadataAndFailureRollback(t *testing.T) {
	t.Run("healthy firewall precedes ready metadata", func(t *testing.T) {
		var events []string
		fw := &fakeFirewall{events: &events}
		publisher := &fakePublisher{events: &events}
		status := health.NewStatus()
		s := Supervisor{Firewall: fw, Publisher: publisher, Status: status, current: "gen-one"}
		if err := s.promoteReady(context.Background(), activeEndpoint()); err != nil {
			t.Fatal(err)
		}
		if strings.Join(events, ",") != "firewall:HEALTHY,publish-ready" || !status.Ready() {
			t.Fatalf("events=%v ready=%v", events, status.Ready())
		}
	})
	t.Run("ready publication failure reverts firewall", func(t *testing.T) {
		var events []string
		fw := &fakeFirewall{events: &events}
		publisher := &fakePublisher{publishReadyErr: errors.New("publish failed"), events: &events}
		status := health.NewStatus()
		s := Supervisor{Firewall: fw, Publisher: publisher, Status: status, current: "gen-one"}
		if err := s.promoteReady(context.Background(), activeEndpoint()); err == nil {
			t.Fatal("ready publication failure was ignored")
		}
		if strings.Join(events, ",") != "firewall:HEALTHY,publish-ready,firewall:VERIFYING" || status.Ready() {
			t.Fatalf("events=%v ready=%v", events, status.Ready())
		}
	})
	t.Run("healthy firewall failure locks and stops", func(t *testing.T) {
		fw := &fakeFirewall{applyErrors: map[int]error{0: errors.New("healthy failed")}}
		publisher := &fakePublisher{}
		child := &fakeChild{done: make(chan error)}
		s := Supervisor{Config: config.Config{ShutdownGrace: time.Second}, Firewall: fw, Publisher: publisher, Status: health.NewStatus(), child: child, current: "gen-one"}
		if err := s.promoteReady(context.Background(), activeEndpoint()); err == nil {
			t.Fatal("healthy firewall failure was ignored")
		}
		if strings.Join(firewallStates(fw.states), ",") != "HEALTHY,LOCKED" || child.stops != 1 || publisher.ready != 0 {
			t.Fatalf("states=%v stops=%d ready publications=%d", fw.states, child.stops, publisher.ready)
		}
	})
	t.Run("rollback failure locks and stops", func(t *testing.T) {
		fw := &fakeFirewall{applyErrors: map[int]error{1: errors.New("verifying failed")}}
		publisher := &fakePublisher{publishReadyErr: errors.New("publish failed")}
		child := &fakeChild{done: make(chan error)}
		s := Supervisor{Config: config.Config{ShutdownGrace: time.Second}, Firewall: fw, Publisher: publisher, Status: health.NewStatus(), child: child, current: "gen-one"}
		if err := s.promoteReady(context.Background(), activeEndpoint()); err == nil {
			t.Fatal("rollback failure was ignored")
		}
		if strings.Join(firewallStates(fw.states), ",") != "HEALTHY,VERIFYING,LOCKED" || child.stops != 1 {
			t.Fatalf("states=%v stops=%d", fw.states, child.stops)
		}
	})
}

func TestHealthRestrictionInjectedFailuresAreFailClosed(t *testing.T) {
	t.Run("restricted firewall failure locks and stops before metadata", func(t *testing.T) {
		var events []string
		fw := &fakeFirewall{applyErrors: map[int]error{0: errors.New("verifying failed")}, events: &events}
		publisher := &fakePublisher{events: &events}
		child := &fakeChild{done: make(chan error), events: &events}
		status := health.NewStatus()
		status.Set(string(StateHealthy), true)
		s := Supervisor{Config: config.Config{ShutdownGrace: time.Second}, Firewall: fw, Publisher: publisher, Status: status, child: child, current: "gen-one", pfPort: 49152}
		if err := s.restrictForVerification(context.Background(), activeEndpoint()); err == nil {
			t.Fatal("restricted firewall failure was ignored")
		}
		if strings.Join(events, ",") != "firewall:VERIFYING,firewall:LOCKED,stop-child" || status.Ready() || publisher.invalidated != 0 || s.pfPort != 0 {
			t.Fatalf("events=%v ready=%v invalidated=%d port=%d", events, status.Ready(), publisher.invalidated, s.pfPort)
		}
	})
	t.Run("ready invalidation failure propagates", func(t *testing.T) {
		fw := &fakeFirewall{}
		publisher := &fakePublisher{invalidateReadyErr: errors.New("invalidate failed")}
		status := health.NewStatus()
		status.Set(string(StateHealthy), true)
		s := Supervisor{Firewall: fw, Publisher: publisher, Status: status, current: "gen-one"}
		if err := s.restrictForVerification(context.Background(), activeEndpoint()); err == nil || status.Ready() {
			t.Fatalf("error=%v ready=%v", err, status.Ready())
		}
	})
}

func TestFailoverAndShutdownSurfaceAllSecurityErrors(t *testing.T) {
	for _, state := range []State{StateFailingOver, StateShuttingDown} {
		t.Run(string(state)+" prerequisites", func(t *testing.T) {
			var events []string
			fw := &fakeFirewall{applyErrors: map[int]error{0: errors.New("lock failed")}, events: &events}
			publisher := &fakePublisher{invalidateReadyErr: errors.New("ready failed"), invalidateCurrentErr: errors.New("current failed"), events: &events}
			child := &fakeChild{done: make(chan error), stopErr: errors.New("stop failed"), events: &events}
			s := Supervisor{Config: config.Config{ShutdownGrace: time.Second}, Firewall: fw, Publisher: publisher, Status: health.NewStatus(), child: child, current: "gen-one", pfPort: 49152}
			err := s.deactivate(state)
			for _, message := range []string{"lock failed", "ready failed", "current failed", "stop failed"} {
				if err == nil || !strings.Contains(err.Error(), message) {
					t.Fatalf("missing %q in %v", message, err)
				}
			}
			if strings.Join(events, ",") != "firewall:LOCKED,stop-child,invalidate-ready,invalidate-current" || s.pfPort != 0 || !s.cleanupRequired || s.child == nil || publisher.removed != 0 {
				t.Fatalf("events=%v port=%d cleanup=%v child=%v removals=%d", events, s.pfPort, s.cleanupRequired, s.child, publisher.removed)
			}
		})
		t.Run(string(state)+" removal", func(t *testing.T) {
			publisher := &fakePublisher{removeErr: errors.New("remove failed")}
			s := Supervisor{Config: config.Config{ShutdownGrace: time.Second}, Firewall: &fakeFirewall{}, Publisher: publisher, Status: health.NewStatus(), child: &fakeChild{done: make(chan error)}, current: "gen-one"}
			if err := s.deactivate(state); err == nil || !strings.Contains(err.Error(), "remove failed") || s.child != nil || s.current != "gen-one" || !s.cleanupRequired {
				t.Fatalf("error=%v child=%v current=%q cleanup=%v", err, s.child, s.current, s.cleanupRequired)
			}
		})
	}
}

func TestStopChildRetainsUnconfirmedProcessAndCleanupRetries(t *testing.T) {
	t.Run("stopChild retains handle", func(t *testing.T) {
		child := &fakeChild{done: make(chan error), stopErrors: []error{errors.New("signal failed"), nil}}
		s := Supervisor{Config: config.Config{ShutdownGrace: time.Second}, child: child}
		if err := s.stopChild(); err == nil || s.child != child {
			t.Fatalf("first stop error=%v child=%v", err, s.child)
		}
		if err := s.stopChild(); err != nil || s.child != nil || child.stops != 2 {
			t.Fatalf("retry error=%v child=%v stops=%d", err, s.child, child.stops)
		}
	})
	t.Run("cleanupRequired retries termination", func(t *testing.T) {
		child := &fakeChild{done: make(chan error), stopErrors: []error{errors.New("signal failed"), nil}}
		publisher := &fakePublisher{}
		s := Supervisor{Config: config.Config{ShutdownGrace: time.Second}, Firewall: &fakeFirewall{}, Publisher: publisher, Status: health.NewStatus(), child: child, current: "gen-one", Now: func() time.Time { return time.Unix(1, 0) }, Sleep: func(context.Context, time.Duration) error { return nil }}
		if err := s.deactivate(StateFailingOver); err == nil || !s.cleanupRequired || s.child != child || publisher.removed != 0 {
			t.Fatalf("first cleanup error=%v required=%v child=%v removals=%d", err, s.cleanupRequired, s.child, publisher.removed)
		}
		if err := s.retryCandidateCleanup(context.Background()); err != nil {
			t.Fatal(err)
		}
		if child.stops != 2 || s.child != nil || s.current != "" || s.cleanupRequired || publisher.removed != 1 {
			t.Fatalf("stops=%d child=%v current=%q cleanup=%v removals=%d", child.stops, s.child, s.current, s.cleanupRequired, publisher.removed)
		}
	})
}

func firewallStates(states []firewall.State) []string {
	out := make([]string, len(states))
	for i, state := range states {
		out[i] = string(state)
	}
	return out
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

func TestOSProcessStopCompletionAndSignals(t *testing.T) {
	start := func(body string) *osChild {
		t.Helper()
		child, err := (OSProcess{}).Start(script(t, body), os.Environ())
		if err != nil {
			t.Fatal(err)
		}
		return child.(*osChild)
	}
	t.Run("child exits before Stop without a post-reap signal", func(t *testing.T) {
		child := start("exit 0\n")
		<-child.waitDone
		signals := 0
		child.signalFn = func(syscall.Signal) error { signals++; return errors.New("post-reap signal") }
		if err := child.Stop(time.Second); err != nil || signals != 0 {
			t.Fatalf("stop error=%v signals=%d", err, signals)
		}
	})
	t.Run("Done consumed before Stop", func(t *testing.T) {
		child := start("exit 0\n")
		if err := <-child.Done(); err != nil {
			t.Fatal(err)
		}
		signals := 0
		child.signalFn = func(syscall.Signal) error { signals++; return errors.New("post-reap signal") }
		if err := child.Stop(time.Second); err != nil || signals != 0 {
			t.Fatalf("stop error=%v signals=%d", err, signals)
		}
	})
	t.Run("natural nonzero exit is gone after Done consumption", func(t *testing.T) {
		child := start("exit 7\n")
		if err := <-child.Done(); err == nil {
			t.Fatal("natural child failure was not reported")
		}
		signals := 0
		child.signalFn = func(syscall.Signal) error { signals++; return errors.New("post-reap signal") }
		if err := child.Stop(time.Second); err != nil || signals != 0 {
			t.Fatalf("stop error=%v signals=%d", err, signals)
		}
	})
	t.Run("transient SIGTERM failure is retryable", func(t *testing.T) {
		child := start("trap 'exit 0' TERM INT\nwhile :; do sleep 1; done\n")
		time.Sleep(25 * time.Millisecond)
		original := child.signalFn
		child.signalFn = func(syscall.Signal) error { return errors.New("transient SIGTERM failure") }
		if err := child.Stop(time.Second); err == nil {
			t.Fatal("transient SIGTERM failure was cached as success")
		}
		select {
		case <-child.waitDone:
			t.Fatal("child exited despite failed SIGTERM delivery")
		default:
		}
		child.signalFn = original
		if err := child.Stop(time.Second); err != nil {
			t.Fatalf("retry stop: %v", err)
		}
	})
	t.Run("repeated Stop sends one graceful signal", func(t *testing.T) {
		child := start("trap 'exit 0' TERM INT\nwhile :; do sleep 1; done\n")
		time.Sleep(25 * time.Millisecond)
		original := child.signalFn
		signals := 0
		child.signalFn = func(signal syscall.Signal) error { signals++; return original(signal) }
		if err := child.Stop(time.Second); err != nil {
			t.Fatal(err)
		}
		if err := child.Stop(time.Second); err != nil || signals != 1 {
			t.Fatalf("repeated stop error=%v signals=%d", err, signals)
		}
	})
	t.Run("SIGKILL is bounded and only sent while running", func(t *testing.T) {
		child := start("trap '' TERM INT\nwhile :; do sleep 1; done\n")
		time.Sleep(25 * time.Millisecond)
		original := child.signalFn
		var signals []syscall.Signal
		child.signalFn = func(signal syscall.Signal) error {
			signals = append(signals, signal)
			if signal == syscall.SIGTERM {
				return nil
			}
			return original(signal)
		}
		started := time.Now()
		err := child.Stop(50 * time.Millisecond)
		if time.Since(started) > time.Second || len(signals) != 2 || signals[0] != syscall.SIGTERM || signals[1] != syscall.SIGKILL {
			t.Fatalf("duration=%s signals=%v error=%v", time.Since(started), signals, err)
		}
		if err != nil {
			t.Fatalf("expected SIGKILL fallback returned %v", err)
		}
	})
	t.Run("transient SIGKILL failure is retryable", func(t *testing.T) {
		child := start("trap '' TERM INT\nwhile :; do sleep 1; done\n")
		time.Sleep(25 * time.Millisecond)
		original := child.signalFn
		killAttempts := 0
		child.signalFn = func(signal syscall.Signal) error {
			if signal == syscall.SIGKILL {
				killAttempts++
				return errors.New("transient SIGKILL failure")
			}
			return nil
		}
		if err := child.Stop(20 * time.Millisecond); err == nil || killAttempts != 1 {
			t.Fatalf("first stop error=%v kill attempts=%d", err, killAttempts)
		}
		select {
		case <-child.waitDone:
			t.Fatal("child exited despite failed SIGKILL delivery")
		default:
		}
		child.signalFn = original
		if err := child.Stop(20 * time.Millisecond); err != nil {
			t.Fatalf("retry stop: %v", err)
		}
	})
}

func script(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "child.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
