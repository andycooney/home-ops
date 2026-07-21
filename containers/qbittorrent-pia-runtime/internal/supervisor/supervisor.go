package supervisor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/andycooney/home-ops/containers/qbittorrent-pia-runtime/internal/api"
	"github.com/andycooney/home-ops/containers/qbittorrent-pia-runtime/internal/config"
	"github.com/andycooney/home-ops/containers/qbittorrent-pia-runtime/internal/firewall"
	"github.com/andycooney/home-ops/containers/qbittorrent-pia-runtime/internal/health"
	"github.com/andycooney/home-ops/containers/qbittorrent-pia-runtime/internal/session"
	"github.com/andycooney/home-ops/containers/qbittorrent-pia-runtime/internal/wireguard"
)

type State string

const (
	StateBootstrap            State = "BOOTSTRAP"
	StateSelecting            State = "SELECTING_SERVER"
	StateRegistering          State = "REGISTERING_WIREGUARD"
	StateStarting             State = "STARTING_TUNNEL"
	StateVerifying            State = "VERIFYING_TUNNEL"
	StateHealthy              State = "HEALTHY"
	StateFailingOver          State = "FAILING_OVER"
	StateAuthenticationFailed State = "AUTHENTICATION_FAILED"
	StateBackoff              State = "BACKOFF"
	StateShuttingDown         State = "SHUTTING_DOWN"
)

type API interface {
	FetchServerList(context.Context) (api.ServerList, error)
	Probe(context.Context, api.Candidate) error
	Token(context.Context, string, string) (string, error)
	Register(context.Context, api.Candidate, string, string) (wireguard.Registration, error)
}
type Firewall interface {
	Init(context.Context) error
	Apply(context.Context, firewall.State, firewall.Endpoint) error
}
type Publisher interface {
	PublishCurrent(session.Generation) (string, error)
	PublishReady(string) error
	InvalidateReady() error
	InvalidateCurrent() error
	Remove(string) error
}
type TunnelVerifier interface {
	Verify(context.Context, netip.Addr) (health.Result, error)
}
type Child interface {
	Done() <-chan error
	Stop(time.Duration) error
}
type Process interface {
	Start(string, []string) (Child, error)
}
type PublicIP func(context.Context, string, time.Duration) (netip.Addr, error)

type Supervisor struct {
	Config    config.Config
	API       API
	Firewall  Firewall
	Publisher Publisher
	Verifier  TunnelVerifier
	Process   Process
	Status    *health.Status
	PublicIP  PublicIP
	Logger    *log.Logger
	Now       func() time.Time
	Sleep     func(context.Context, time.Duration) error
	cooldown  map[string]time.Time
	child     Child
	current   string
}

func (s *Supervisor) Run(ctx context.Context) error {
	if err := s.defaults(); err != nil {
		return err
	}
	s.transition(StateBootstrap, false)
	if err := s.Firewall.Init(ctx); err != nil {
		s.Status.Fail()
		return fmt.Errorf("install fail-closed firewall: %w", err)
	}
	preTunnelIP, err := s.bootstrapPublicIP(ctx)
	if err != nil {
		return s.shutdown()
	}
	return s.backoffLoop(ctx, preTunnelIP)
}

func (s *Supervisor) bootstrapPublicIP(ctx context.Context) (netip.Addr, error) {
	backoff := 30 * time.Second
	for ctx.Err() == nil {
		s.transition(StateBootstrap, false)
		preTunnelIP, err := s.PublicIP(ctx, s.Config.PublicIPURL, s.Config.ProbeTimeout)
		if err == nil && preTunnelIP.IsValid() {
			return preTunnelIP, nil
		}
		s.log("bootstrap public-IP verification failed")
		s.transition(StateBackoff, false)
		delay := jitter(backoff, s.Now())
		s.log("bootstrap failure; retrying in %s", delay)
		if err := s.Sleep(ctx, delay); err != nil {
			return netip.Addr{}, err
		}
		backoff = nextBackoff(backoff)
	}
	return netip.Addr{}, ctx.Err()
}

func (s *Supervisor) backoffLoop(ctx context.Context, preTunnelIP netip.Addr) error {
	backoff := 30 * time.Second
	for ctx.Err() == nil {
		err := s.runCycle(ctx, preTunnelIP)
		if ctx.Err() != nil {
			break
		}
		if api.IsAuthentication(err) {
			s.transition(StateAuthenticationFailed, false)
			s.log("authentication failed; credentials were not logged")
			if err := s.Sleep(ctx, s.Config.AuthRetry); err != nil {
				break
			}
			backoff = 30 * time.Second
			continue
		}
		s.transition(StateBackoff, false)
		delay := jitter(backoff, s.Now())
		s.log("recoverable cycle failure; retrying in %s", delay)
		if err := s.Sleep(ctx, delay); err != nil {
			break
		}
		backoff = nextBackoff(backoff)
	}
	return s.shutdown()
}

func (s *Supervisor) runCycle(ctx context.Context, preTunnelIP netip.Addr) error {
	s.transition(StateSelecting, false)
	if err := s.Firewall.Apply(ctx, firewall.Bootstrap, firewall.Endpoint{}); err != nil {
		return err
	}
	list, err := s.API.FetchServerList(ctx)
	if err != nil {
		return err
	}
	candidates := api.SelectCandidates(list, s.Config.PreferredRegions, s.cooldown, s.Now(), s.Config.CandidateMax)
	s.log("eligible candidate count=%d", len(candidates))
	if len(candidates) == 0 {
		return errors.New("no eligible candidates")
	}
	var lastErr error
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.Firewall.Apply(ctx, firewall.Bootstrap, firewall.Endpoint{}); err != nil {
			return err
		}
		token, err := s.API.Token(ctx, s.Config.Username, s.Config.Password)
		if err != nil {
			if api.IsAuthentication(err) {
				return err
			}
			lastErr = err
			s.cool(candidate)
			continue
		}
		keys, err := wireguard.GenerateKeyPair()
		if err != nil {
			return err
		}
		registrationEndpoint := firewall.Endpoint{IP: netip.MustParseAddr(candidate.IP), Port: candidate.Port}
		if err := s.Firewall.Apply(ctx, firewall.Selected, registrationEndpoint); err != nil {
			return err
		}
		if err := s.API.Probe(ctx, candidate); err != nil {
			lastErr = err
			s.cool(candidate)
			continue
		}
		s.transition(StateRegistering, false)
		registration, err := s.API.Register(ctx, candidate, token, keys.Public)
		if err != nil {
			if api.IsAuthentication(err) {
				return err
			}
			lastErr = err
			s.cool(candidate)
			continue
		}
		wgConfig, err := wireguard.BuildConfig(keys, candidate.IP, registration)
		if err != nil {
			lastErr = err
			s.cool(candidate)
			continue
		}
		generationID, err := newGenerationID(s.Now())
		if err != nil {
			return err
		}
		generation := session.Generation{ID: generationID, Region: candidate.RegionID, Endpoint: netip.AddrPortFrom(netip.MustParseAddr(candidate.IP), registration.ServerPort).String(), TLSHostname: candidate.Hostname, WGGateway: registration.ServerIP, PFGateway: candidate.IP, Token: token, WGConfig: wgConfig}
		dir, err := s.Publisher.PublishCurrent(generation)
		if err != nil {
			return err
		}
		s.current = generationID
		tunnelEndpoint := firewall.Endpoint{IP: netip.MustParseAddr(candidate.IP), Port: registration.ServerPort}
		if err := s.startAndVerify(ctx, dir, registration.DNSServers[0], tunnelEndpoint, preTunnelIP); err != nil {
			lastErr = err
			s.failover(candidate)
			continue
		}
		if err := s.monitorHealthy(ctx, candidate, tunnelEndpoint, preTunnelIP); err != nil {
			lastErr = err
			s.failover(candidate)
			continue
		}
		return nil
	}
	if lastErr == nil {
		lastErr = errors.New("candidate attempts exhausted")
	}
	return lastErr
}

func (s *Supervisor) startAndVerify(ctx context.Context, dir, dns string, endpoint firewall.Endpoint, preTunnelIP netip.Addr) error {
	s.transition(StateStarting, false)
	if err := s.Firewall.Apply(ctx, firewall.Verifying, endpoint); err != nil {
		return err
	}
	env := ChildEnvironment(os.Environ(), dir+"/wg0.conf")
	child, err := s.Process.Start(s.Config.GluetunEntrypoint, env)
	if err != nil {
		return errors.New("start Gluetun child failed")
	}
	s.child = child
	deadline := s.Now().Add(s.Config.TunnelTimeout)
	for s.Now().Before(deadline) {
		s.transition(StateVerifying, false)
		select {
		case err := <-child.Done():
			if err == nil {
				err = errors.New("gluetun exited")
			}
			return err
		default:
		}
		if verifier, ok := s.Verifier.(*health.Verifier); ok {
			verifier.DNSAddress = dns
		}
		if _, err := s.Verifier.Verify(ctx, preTunnelIP); err == nil {
			if err := s.Publisher.PublishReady(s.current); err != nil {
				return err
			}
			if err := s.Firewall.Apply(ctx, firewall.Healthy, endpoint); err != nil {
				_ = s.Publisher.InvalidateReady()
				return err
			}
			s.transition(StateHealthy, true)
			return nil
		}
		if err := s.Sleep(ctx, 5*time.Second); err != nil {
			return err
		}
	}
	return errors.New("tunnel verification timeout")
}

func (s *Supervisor) monitorHealthy(ctx context.Context, candidate api.Candidate, endpoint firewall.Endpoint, preTunnelIP netip.Addr) error {
	started := s.Now()
	failures := 0
	for {
		if s.Now().Sub(started) >= s.Config.SessionMaxAge {
			return errors.New("proactive session rotation")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-s.child.Done():
			if err == nil {
				err = errors.New("gluetun exited")
			}
			return err
		default:
		}
		if err := s.Sleep(ctx, s.Config.HealthInterval); err != nil {
			return err
		}
		_, err := s.Verifier.Verify(ctx, preTunnelIP)
		if err == nil {
			failures = 0
			if !s.Status.Ready() {
				if err := s.Publisher.PublishReady(s.current); err != nil {
					return err
				}
				if err := s.Firewall.Apply(ctx, firewall.Healthy, endpoint); err != nil {
					return err
				}
				s.transition(StateHealthy, true)
			}
			continue
		}
		failures++
		if failures == 1 {
			_ = s.Publisher.InvalidateReady()
			_ = s.Firewall.Apply(ctx, firewall.Verifying, endpoint)
			s.transition(StateVerifying, false)
		}
		if failures >= s.Config.HealthFailures {
			return fmt.Errorf("health failure threshold reached for %s", candidate.RegionID)
		}
	}
}

func (s *Supervisor) failover(candidate api.Candidate) {
	s.transition(StateFailingOver, false)
	_ = s.Publisher.InvalidateReady()
	_ = s.Publisher.InvalidateCurrent()
	_ = s.Firewall.Apply(context.Background(), firewall.Locked, firewall.Endpoint{})
	if s.child != nil {
		_ = s.child.Stop(s.Config.ShutdownGrace)
		s.child = nil
	}
	if s.current != "" {
		_ = s.Publisher.Remove(s.current)
		s.current = ""
	}
	s.cool(candidate)
}
func (s *Supervisor) shutdown() error {
	s.transition(StateShuttingDown, false)
	_ = s.Publisher.InvalidateReady()
	_ = s.Publisher.InvalidateCurrent()
	_ = s.Firewall.Apply(context.Background(), firewall.Locked, firewall.Endpoint{})
	if s.child != nil {
		_ = s.child.Stop(s.Config.ShutdownGrace)
	}
	if s.current != "" {
		_ = s.Publisher.Remove(s.current)
	}
	return nil
}
func (s *Supervisor) transition(state State, ready bool) {
	s.Status.Set(string(state), ready)
	s.log("state=%s", state)
}
func (s *Supervisor) cool(candidate api.Candidate) {
	s.cooldown[candidate.IP] = s.Now().Add(10 * time.Minute)
}
func (s *Supervisor) log(format string, args ...any) {
	if s.Logger != nil {
		s.Logger.Printf(format, args...)
	}
}
func (s *Supervisor) defaults() error {
	if s.API == nil || s.Firewall == nil || s.Publisher == nil || s.Verifier == nil || s.Process == nil {
		return errors.New("supervisor dependencies are incomplete")
	}
	if s.Status == nil {
		s.Status = health.NewStatus()
	}
	if s.PublicIP == nil {
		s.PublicIP = health.PreTunnelPublicIP
	}
	if s.Now == nil {
		s.Now = time.Now
	}
	if s.Sleep == nil {
		s.Sleep = func(ctx context.Context, duration time.Duration) error {
			return sleepHeartbeat(ctx, duration, s.Status)
		}
	}
	if s.cooldown == nil {
		s.cooldown = map[string]time.Time{}
	}
	return nil
}

func ChildEnvironment(source []string, wgConfigPath string) []string {
	blocked := []string{"PIA_", "TOKEN", "PASS", "SECRET", "AUTH", "CREDENTIAL", "ACCESS_KEY", "API_KEY", "PRIVATE_KEY", "USERNAME", "OPENVPN_USER", "HTTPPROXY_USER", "UPDATER_PROTONVPN_EMAIL"}
	overrideValues := []string{
		"VPN_SERVICE_PROVIDER=custom", "VPN_TYPE=wireguard", "WIREGUARD_CONF_SECRETFILE=" + wgConfigPath,
		"FIREWALL_ENABLED_DISABLING_IT_SHOOTS_YOU_IN_YOUR_FOOT=off", "HEALTH_RESTART_VPN=off",
		"PUBLICIP_ENABLED=off", "VERSION_INFORMATION=off", "VPN_PORT_FORWARDING=off",
	}
	overrideKeys := make(map[string]struct{}, len(overrideValues))
	for _, entry := range overrideValues {
		key, _, _ := strings.Cut(entry, "=")
		overrideKeys[key] = struct{}{}
	}
	out := make([]string, 0, len(source)+8)
	for _, entry := range source {
		key, _, _ := strings.Cut(entry, "=")
		if _, overridden := overrideKeys[key]; overridden {
			continue
		}
		upper := strings.ToUpper(key)
		deny := false
		for _, needle := range blocked {
			if strings.Contains(upper, needle) {
				deny = true
				break
			}
		}
		if !deny {
			out = append(out, entry)
		}
	}
	out = append(out, overrideValues...)
	return out
}

type OSProcess struct{}
type osChild struct {
	cmd  *exec.Cmd
	done chan error
	once sync.Once
}

func (OSProcess) Start(path string, env []string) (Child, error) {
	cmd := exec.Command(path)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	child := &osChild{cmd: cmd, done: make(chan error, 1)}
	go func() { child.done <- cmd.Wait(); close(child.done) }()
	return child, nil
}
func (c *osChild) Done() <-chan error { return c.done }
func (c *osChild) Stop(grace time.Duration) error {
	var result error
	c.once.Do(func() {
		select {
		case result = <-c.done:
			_ = c.signal(syscall.SIGKILL)
			reapOrphans(500 * time.Millisecond)
			return
		default:
		}
		_ = c.signal(syscall.SIGTERM)
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case result = <-c.done:
			if status, ok := c.cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() && status.Signal() == syscall.SIGTERM {
				result = nil
			}
		case <-timer.C:
			_ = c.signal(syscall.SIGKILL)
			result = <-c.done
		}
		reapOrphans(500 * time.Millisecond)
	})
	return result
}
func (c *osChild) signal(signal syscall.Signal) error {
	if err := syscall.Kill(-c.cmd.Process.Pid, signal); err == nil {
		return nil
	}
	return c.cmd.Process.Signal(signal)
}

func reapOrphans(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var status syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
		if err == syscall.ECHILD {
			return
		}
		if err != nil {
			return
		}
		if pid == 0 {
			time.Sleep(10 * time.Millisecond)
			continue
		}
	}
}

func newGenerationID(now time.Time) (string, error) {
	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(random), nil
}
func jitter(base time.Duration, now time.Time) time.Duration {
	const spread = 20
	offset := int(now.UnixNano()%int64(2*spread+1)) - spread
	return base + time.Duration(offset)*base/100
}
func nextBackoff(current time.Duration) time.Duration {
	current *= 2
	if current > 5*time.Minute {
		return 5 * time.Minute
	}
	return current
}
func sleepHeartbeat(ctx context.Context, duration time.Duration, status *health.Status) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			status.Beat()
			return nil
		case <-ticker.C:
			status.Beat()
		}
	}
}
