package supervisor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
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
	ReadForwardedPort(string) (uint16, error)
	InvalidateReady() error
	InvalidateCurrent() error
	Remove(string) error
}
type TunnelVerifier interface {
	Verify(context.Context, netip.Addr) (health.Result, error)
}
type Child interface {
	Done() <-chan error
	// Stop returns nil only after the child has been reaped. A non-nil result
	// leaves termination unconfirmed and must remain retryable.
	Stop(time.Duration) error
}
type Process interface {
	Start(string, []string) (Child, error)
}
type PublicIP func(context.Context, string, time.Duration) (netip.Addr, error)

// PIA's account tokens are reusable and normally valid for 24 hours. Refresh
// slightly early so endpoint rotation does not repeatedly call generateToken or
// attempt registration with a token at the edge of its validity window.
const tokenReuseLifetime = 23 * time.Hour

type failureClass uint8

const (
	failureLocal failureClass = iota
	failureCandidate
	failureRefresh
	failureRotation
	failureGlobal
	failureAuthentication
)

type classifiedFailure struct {
	class failureClass
	err   error
}

func (e *classifiedFailure) Error() string { return e.err.Error() }
func (e *classifiedFailure) Unwrap() error { return e.err }

func (c failureClass) String() string {
	switch c {
	case failureCandidate:
		return "candidate"
	case failureRefresh:
		return "endpoint-refresh"
	case failureRotation:
		return "rotation"
	case failureGlobal:
		return "global-service"
	case failureAuthentication:
		return "authentication"
	default:
		return "local-runtime"
	}
}

func classifyFailure(err error) failureClass {
	if api.IsAuthentication(err) {
		return failureAuthentication
	}
	var classified *classifiedFailure
	if errors.As(err, &classified) {
		return classified.class
	}
	return failureLocal
}

func candidateFailure(err error) error { return &classifiedFailure{class: failureCandidate, err: err} }
func refreshFailure(err error) error   { return &classifiedFailure{class: failureRefresh, err: err} }
func rotationFailure(err error) error  { return &classifiedFailure{class: failureRotation, err: err} }
func globalFailure(err error) error    { return &classifiedFailure{class: failureGlobal, err: err} }
func localFailure(err error) error     { return &classifiedFailure{class: failureLocal, err: err} }

type Supervisor struct {
	Config          config.Config
	API             API
	Firewall        Firewall
	Publisher       Publisher
	Verifier        TunnelVerifier
	Process         Process
	Status          *health.Status
	PublicIP        PublicIP
	Logger          *log.Logger
	Now             func() time.Time
	Sleep           func(context.Context, time.Duration) error
	cooldown        map[string]time.Time
	child           Child
	current         string
	pfPort          uint16
	cleanupRequired bool
	authToken       string
	authTokenExpiry time.Time
	cycle           func(context.Context, netip.Addr) error
	attempt         func(context.Context, api.Candidate, netip.Addr) error
	generateKeyPair func() (wireguard.KeyPair, error)
	cleanupNetwork  func(string) error
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
	return s.backoffLoop(ctx)
}

func (s *Supervisor) bootstrapPublicIP(ctx context.Context) (netip.Addr, error) {
	backoff := 30 * time.Second
	for ctx.Err() == nil {
		s.transition(StateBootstrap, false)
		if err := s.Firewall.Apply(ctx, firewall.Bootstrap, firewall.Endpoint{}); err != nil {
			return netip.Addr{}, s.lockAndStop(fmt.Errorf("apply bootstrap firewall: %w", err))
		}
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

func (s *Supervisor) backoffLoop(ctx context.Context) error {
	backoff := 30 * time.Second
	for ctx.Err() == nil {
		if s.cleanupRequired {
			if err := s.failover(); err != nil {
				s.transition(StateBackoff, false)
				delay := jitter(backoff, s.Now())
				s.log("fail-closed cleanup failed; retrying in %s", delay)
				if err := s.Sleep(ctx, delay); err != nil {
					break
				}
				backoff = nextBackoff(backoff)
				continue
			}
		}
		preTunnelIP, err := s.bootstrapPublicIP(ctx)
		if err == nil {
			cycle := s.runCycle
			if s.cycle != nil {
				cycle = s.cycle
			}
			err = cycle(ctx, preTunnelIP)
		}
		if ctx.Err() != nil {
			break
		}
		cleanupErr := s.failover()
		if cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
		failure := classifyFailure(err)
		if cleanupErr == nil && (failure == failureRefresh || failure == failureRotation) {
			s.log("session ended classification=%s; refreshing server metadata immediately", failure)
			backoff = 30 * time.Second
			continue
		}
		if cleanupErr == nil && api.IsAuthentication(err) {
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
		s.log("recoverable cycle failure classification=%s reason=%s; retrying in %s", failure, err, delay)
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
	requiredAttempts := min(s.Config.CandidateMin, len(candidates))
	var lastErr error
	attempts := 0
	comparisonIP := preTunnelIP
	for index, candidate := range candidates {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		attempts++
		s.log("candidate attempt=%d required-minimum=%d maximum=%d region_id=%s region_name=%q advertised_ip=%s tls_hostname=%s port=%d", attempts, requiredAttempts, len(candidates), candidate.RegionID, candidate.RegionName, candidate.IP, candidate.Hostname, candidate.Port)
		attempt := s.attemptCandidate
		if s.attempt != nil {
			attempt = s.attempt
		}
		err := attempt(ctx, candidate, comparisonIP)
		if err == nil {
			return nil
		}
		failure := classifyFailure(err)
		s.log("candidate attempt failed classification=%s", failure)
		if failure == failureAuthentication {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if failure == failureRefresh {
			s.cool(candidate)
			return err
		}
		if failure == failureRotation {
			return err
		}
		lastErr = err
		if cleanupErr := s.retryCandidateCleanup(ctx); cleanupErr != nil {
			return errors.Join(err, cleanupErr)
		}
		if failure != failureCandidate {
			return err
		}
		if failure == failureCandidate {
			s.cool(candidate)
		}
		if index+1 < len(candidates) {
			freshIP, bootstrapErr := s.bootstrapPublicIP(ctx)
			if bootstrapErr != nil {
				return errors.Join(err, bootstrapErr)
			}
			comparisonIP = freshIP
		}
	}
	if lastErr == nil {
		lastErr = errors.New("candidate attempts exhausted")
	}
	if attempts < requiredAttempts {
		return fmt.Errorf("candidate minimum not reached: attempted %d of %d: %w", attempts, requiredAttempts, lastErr)
	}
	return fmt.Errorf("candidate attempts exhausted after %d attempts (minimum %d): %w", attempts, requiredAttempts, lastErr)
}

func (s *Supervisor) retryCandidateCleanup(ctx context.Context) error {
	backoff := 30 * time.Second
	for ctx.Err() == nil {
		if err := s.failover(); err != nil {
			s.transition(StateBackoff, false)
			delay := jitter(backoff, s.Now())
			s.log("candidate cleanup failed; retrying in %s", delay)
			if sleepErr := s.Sleep(ctx, delay); sleepErr != nil {
				return errors.Join(err, sleepErr)
			}
			backoff = nextBackoff(backoff)
			continue
		}
		return nil
	}
	return ctx.Err()
}

func (s *Supervisor) attemptCandidate(ctx context.Context, candidate api.Candidate, preTunnelIP netip.Addr) error {
	registrationEndpoint := firewall.Endpoint{IP: netip.MustParseAddr(candidate.IP), Port: candidate.Port}
	if err := s.Firewall.Apply(ctx, firewall.Selected, registrationEndpoint); err != nil {
		return localFailure(err)
	}
	if err := s.API.Probe(ctx, candidate); err != nil {
		return candidateFailure(err)
	}
	if err := s.Firewall.Apply(ctx, firewall.Bootstrap, firewall.Endpoint{}); err != nil {
		return localFailure(err)
	}
	token, err := s.token(ctx)
	if err != nil {
		if api.IsAuthentication(err) {
			return err
		}
		return globalFailure(err)
	}
	generateKeyPair := wireguard.GenerateKeyPair
	if s.generateKeyPair != nil {
		generateKeyPair = s.generateKeyPair
	}
	keys, err := generateKeyPair()
	if err != nil {
		return localFailure(err)
	}
	if err := s.Firewall.Apply(ctx, firewall.Selected, registrationEndpoint); err != nil {
		return localFailure(err)
	}
	s.transition(StateRegistering, false)
	registration, err := s.API.Register(ctx, candidate, token, keys.Public)
	if err != nil {
		if api.IsAuthentication(err) {
			s.invalidateToken()
			return globalFailure(errors.New("WireGuard registration rejected cached account token"))
		}
		return candidateFailure(err)
	}
	s.log("wireguard registration region_id=%s advertised_ip=%s server_ip=%s server_port=%d server_vip=%s peer_ip=%s dns_servers=%s", candidate.RegionID, candidate.IP, registration.ServerIP, registration.ServerPort, registration.ServerVIP, registration.PeerIP, strings.Join(registration.DNSServers, ","))
	wgConfig, err := wireguard.BuildConfig(keys, registration)
	if err != nil {
		return candidateFailure(err)
	}
	generationID, err := newGenerationID(s.Now())
	if err != nil {
		return localFailure(err)
	}
	tunnelIP := netip.MustParseAddr(registration.ServerIP)
	pfGateway := netip.MustParseAddr(registration.ServerIP)
	generation := session.Generation{ID: generationID, Region: candidate.RegionID, Endpoint: netip.AddrPortFrom(tunnelIP, registration.ServerPort).String(), TLSHostname: candidate.Hostname, WGGateway: registration.ServerVIP, PFGateway: registration.ServerIP, Token: token, WGConfig: wgConfig}
	dir, err := s.Publisher.PublishCurrent(generation)
	if err != nil {
		return localFailure(err)
	}
	s.current = generationID
	tunnelEndpoint := firewall.Endpoint{IP: tunnelIP, Port: registration.ServerPort, PFGateway: pfGateway}
	if err := s.startAndVerify(ctx, dir, registration.DNSServers[0], tunnelEndpoint, preTunnelIP); err != nil {
		return err
	}
	return s.monitorHealthy(ctx, candidate, tunnelEndpoint, preTunnelIP)
}

func (s *Supervisor) token(ctx context.Context) (string, error) {
	if s.authToken != "" && s.Now().Before(s.authTokenExpiry) {
		return s.authToken, nil
	}
	s.invalidateToken()
	token, err := s.API.Token(ctx, s.Config.Username, s.Config.Password)
	if err != nil {
		return "", err
	}
	s.authToken = token
	s.authTokenExpiry = s.Now().Add(tokenReuseLifetime)
	return token, nil
}

func (s *Supervisor) invalidateToken() {
	s.authToken = ""
	s.authTokenExpiry = time.Time{}
}

func (s *Supervisor) startAndVerify(ctx context.Context, dir, dns string, endpoint firewall.Endpoint, preTunnelIP netip.Addr) error {
	s.transition(StateStarting, false)
	if err := s.Firewall.Apply(ctx, firewall.Verifying, endpoint); err != nil {
		return localFailure(s.lockAndStop(fmt.Errorf("apply verifying firewall: %w", err)))
	}
	env := ChildEnvironment(os.Environ(), dir+"/wg0.conf", s.Config.TunnelUID)
	child, err := s.Process.Start(s.Config.GluetunEntrypoint, env)
	if err != nil {
		return localFailure(errors.New("start Gluetun child failed"))
	}
	s.child = child
	deadline := s.Now().Add(s.Config.TunnelTimeout)
	verificationAttempt := 0
	for s.Now().Before(deadline) {
		verificationAttempt++
		s.transition(StateVerifying, false)
		select {
		case err := <-child.Done():
			if err == nil {
				err = errors.New("gluetun exited")
			}
			return candidateFailure(err)
		default:
		}
		if verifier, ok := s.Verifier.(*health.Verifier); ok {
			verifier.DNSAddress = dns
		}
		result, verifyErr := s.Verifier.Verify(ctx, preTunnelIP)
		if verifyErr == nil {
			s.log("tunnel verification succeeded endpoint=%s public_ip=%s rx_before=%d rx_after=%d tx_before=%d tx_after=%d", netip.AddrPortFrom(endpoint.IP, endpoint.Port), result.PublicIP, result.Before.RX, result.After.RX, result.Before.TX, result.After.TX)
			return s.promoteReady(ctx, endpoint)
		}
		s.log("tunnel verification failed attempt=%d endpoint=%s reason=%s", verificationAttempt, netip.AddrPortFrom(endpoint.IP, endpoint.Port), verifyErr)
		if err := s.Sleep(ctx, 5*time.Second); err != nil {
			return localFailure(err)
		}
	}
	return candidateFailure(errors.New("tunnel verification timeout"))
}

func (s *Supervisor) monitorHealthy(ctx context.Context, candidate api.Candidate, endpoint firewall.Endpoint, preTunnelIP netip.Addr) error {
	started := s.Now()
	failures := 0
	for {
		if s.Now().Sub(started) >= s.Config.SessionMaxAge {
			return rotationFailure(errors.New("proactive session rotation"))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-s.child.Done():
			if err == nil {
				err = errors.New("gluetun exited")
			}
			return refreshFailure(err)
		default:
		}
		if err := s.Sleep(ctx, s.Config.HealthInterval); err != nil {
			return localFailure(err)
		}
		_, err := s.Verifier.Verify(ctx, preTunnelIP)
		if err == nil {
			failures = 0
			if !s.Status.Ready() {
				if err := s.promoteReady(ctx, endpoint); err != nil {
					return localFailure(err)
				}
			}
			if err := s.syncForwardedPort(ctx, endpoint); err != nil {
				return localFailure(err)
			}
			continue
		}
		failures++
		s.log("established tunnel health failed consecutive=%d threshold=%d endpoint=%s reason=%s", failures, s.Config.HealthFailures, netip.AddrPortFrom(endpoint.IP, endpoint.Port), err)
		if failures == 1 {
			if err := s.restrictForVerification(ctx, endpoint); err != nil {
				return localFailure(err)
			}
		}
		if failures >= s.Config.HealthFailures {
			return refreshFailure(fmt.Errorf("health failure threshold reached for %s", candidate.RegionID))
		}
	}
}

func (s *Supervisor) promoteReady(ctx context.Context, endpoint firewall.Endpoint) error {
	endpoint.ForwardedPort = s.pfPort
	if err := s.Firewall.Apply(ctx, firewall.Healthy, endpoint); err != nil {
		return s.lockAndStop(fmt.Errorf("apply healthy firewall: %w", err))
	}
	if err := s.Publisher.PublishReady(s.current); err != nil {
		endpoint.ForwardedPort = 0
		s.pfPort = 0
		if restrictErr := s.Firewall.Apply(ctx, firewall.Verifying, endpoint); restrictErr != nil {
			return s.lockAndStop(errors.Join(fmt.Errorf("publish ready metadata: %w", err), fmt.Errorf("revert verifying firewall: %w", restrictErr)))
		}
		return fmt.Errorf("publish ready metadata: %w", err)
	}
	s.transition(StateHealthy, true)
	return nil
}

func (s *Supervisor) restrictForVerification(ctx context.Context, endpoint firewall.Endpoint) error {
	s.transition(StateVerifying, false)
	endpoint.ForwardedPort = 0
	s.pfPort = 0
	if err := s.Firewall.Apply(ctx, firewall.Verifying, endpoint); err != nil {
		return s.lockAndStop(fmt.Errorf("apply restricted firewall: %w", err))
	}
	if err := s.Publisher.InvalidateReady(); err != nil {
		return fmt.Errorf("invalidate ready metadata: %w", err)
	}
	return nil
}

func (s *Supervisor) syncForwardedPort(ctx context.Context, endpoint firewall.Endpoint) error {
	port, err := s.Publisher.ReadForwardedPort(s.current)
	if errors.Is(err, session.ErrPFPortPending) {
		if s.pfPort != 0 {
			endpoint.ForwardedPort = 0
			if firewallErr := s.Firewall.Apply(ctx, firewall.Healthy, endpoint); firewallErr != nil {
				return s.lockAndStop(fmt.Errorf("revoke unpublished PF port: %w", firewallErr))
			}
			s.pfPort = 0
		}
		return nil
	}
	if err != nil {
		if s.pfPort != 0 {
			endpoint.ForwardedPort = 0
			if firewallErr := s.Firewall.Apply(ctx, firewall.Healthy, endpoint); firewallErr != nil {
				return s.lockAndStop(errors.Join(fmt.Errorf("reject PF port publication: %w", err), fmt.Errorf("revoke PF inbound allowance: %w", firewallErr)))
			}
			s.pfPort = 0
		}
		return fmt.Errorf("reject PF port publication: %w", err)
	}
	if port == s.pfPort {
		return nil
	}
	endpoint.ForwardedPort = port
	if err := s.Firewall.Apply(ctx, firewall.Healthy, endpoint); err != nil {
		return s.lockAndStop(fmt.Errorf("apply PF inbound allowance: %w", err))
	}
	s.pfPort = port
	return nil
}

func (s *Supervisor) lockAndStop(cause error) error {
	s.pfPort = 0
	lockErr := s.Firewall.Apply(context.Background(), firewall.Locked, firewall.Endpoint{})
	stopErr := s.stopChild()
	var networkErr error
	if stopErr == nil {
		networkErr = s.removeTunnelNetwork()
	}
	if lockErr != nil {
		lockErr = fmt.Errorf("apply locked firewall: %w", lockErr)
	}
	if stopErr != nil {
		stopErr = fmt.Errorf("stop Gluetun child: %w", stopErr)
	}
	if networkErr != nil {
		networkErr = fmt.Errorf("remove tunnel network state: %w", networkErr)
	}
	return errors.Join(cause, lockErr, stopErr, networkErr)
}

func (s *Supervisor) stopChild() error {
	if s.child == nil {
		return nil
	}
	if err := s.child.Stop(s.Config.ShutdownGrace); err != nil {
		return err
	}
	s.child = nil
	return nil
}

func (s *Supervisor) failover() error {
	return s.deactivate(StateFailingOver)
}

func (s *Supervisor) shutdown() error {
	return s.deactivate(StateShuttingDown)
}

func (s *Supervisor) deactivate(state State) error {
	s.transition(state, false)
	s.cleanupRequired = true
	s.pfPort = 0
	lockErr := s.Firewall.Apply(context.Background(), firewall.Locked, firewall.Endpoint{})
	var stopErr error
	if lockErr != nil {
		stopErr = s.stopChild()
	}
	readyErr := s.Publisher.InvalidateReady()
	currentErr := s.Publisher.InvalidateCurrent()
	if lockErr == nil {
		stopErr = s.stopChild()
	}
	var networkErr error
	if stopErr == nil {
		networkErr = s.removeTunnelNetwork()
	}
	var removeErr error
	if s.current != "" && stopErr == nil && networkErr == nil && readyErr == nil && currentErr == nil {
		removeErr = s.Publisher.Remove(s.current)
		if removeErr == nil {
			s.current = ""
		}
	}
	if lockErr != nil {
		lockErr = fmt.Errorf("apply locked firewall: %w", lockErr)
	}
	if readyErr != nil {
		readyErr = fmt.Errorf("invalidate ready metadata: %w", readyErr)
	}
	if currentErr != nil {
		currentErr = fmt.Errorf("invalidate current metadata: %w", currentErr)
	}
	if stopErr != nil {
		stopErr = fmt.Errorf("stop Gluetun child: %w", stopErr)
	}
	if networkErr != nil {
		networkErr = fmt.Errorf("remove tunnel network state: %w", networkErr)
	}
	if removeErr != nil {
		removeErr = fmt.Errorf("remove generation: %w", removeErr)
	}
	result := errors.Join(lockErr, readyErr, currentErr, stopErr, networkErr, removeErr)
	if result == nil {
		s.cleanupRequired = false
	}
	return result
}

func (s *Supervisor) removeTunnelNetwork() error {
	if s.Config.Interface == "" {
		return nil
	}
	cleanup := cleanupTunnelNetwork
	if s.cleanupNetwork != nil {
		cleanup = s.cleanupNetwork
	}
	return cleanup(s.Config.Interface)
}

func cleanupTunnelNetwork(interfaceName string) error {
	var result error
	for _, family := range []string{"-4", "-6"} {
		output, err := exec.Command("/sbin/ip", family, "rule", "show", "priority", "101").Output()
		if err != nil {
			result = errors.Join(result, fmt.Errorf("inspect %s WireGuard rule: %w", family, err))
			continue
		}
		if strings.TrimSpace(string(output)) == "" {
			continue
		}
		if err := exec.Command("/sbin/ip", family, "rule", "delete", "priority", "101").Run(); err != nil {
			result = errors.Join(result, fmt.Errorf("delete %s WireGuard rule: %w", family, err))
		}
	}
	if _, err := net.InterfaceByName(interfaceName); err != nil {
		return result
	}
	if err := exec.Command("/sbin/ip", "link", "delete", "dev", interfaceName).Run(); err != nil {
		result = errors.Join(result, fmt.Errorf("delete tunnel interface: %w", err))
	}
	return result
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

func ChildEnvironment(source []string, wgConfigPath string, tunnelUID int) []string {
	blocked := []string{"PIA_", "TOKEN", "PASS", "SECRET", "AUTH", "CREDENTIAL", "ACCESS_KEY", "API_KEY", "PRIVATE_KEY", "USERNAME", "OPENVPN_USER", "HTTPPROXY_USER", "UPDATER_PROTONVPN_EMAIL"}
	overrideValues := []string{
		"VPN_SERVICE_PROVIDER=custom", "VPN_TYPE=wireguard", "WIREGUARD_CONF_SECRETFILE=" + wgConfigPath,
		"WIREGUARD_IMPLEMENTATION=userspace", "PUID=" + strconv.Itoa(tunnelUID), "PGID=" + strconv.Itoa(tunnelUID),
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
	cmd      *exec.Cmd
	done     chan error
	waitDone chan struct{}
	stopMu   sync.Mutex
	signalFn func(syscall.Signal) error
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
	child := &osChild{cmd: cmd, done: make(chan error, 1), waitDone: make(chan struct{})}
	child.signalFn = child.signal
	go func() {
		result := cmd.Wait()
		close(child.waitDone)
		child.done <- result
		close(child.done)
	}()
	return child, nil
}
func (c *osChild) Done() <-chan error { return c.done }
func (c *osChild) Stop(grace time.Duration) error {
	c.stopMu.Lock()
	defer c.stopMu.Unlock()
	return c.stop(grace)
}
func (c *osChild) stop(grace time.Duration) error {
	select {
	case <-c.waitDone:
		reapOrphans(500 * time.Millisecond)
		return nil
	default:
	}
	if err := c.signalFn(syscall.SIGTERM); err != nil {
		select {
		case <-c.waitDone:
			reapOrphans(500 * time.Millisecond)
			return nil
		default:
			return err
		}
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-c.waitDone:
		reapOrphans(500 * time.Millisecond)
		return nil
	case <-timer.C:
	}
	select {
	case <-c.waitDone:
		reapOrphans(500 * time.Millisecond)
		return nil
	default:
	}
	if err := c.signalFn(syscall.SIGKILL); err != nil {
		select {
		case <-c.waitDone:
			reapOrphans(500 * time.Millisecond)
			return nil
		default:
			return err
		}
	}
	<-c.waitDone
	reapOrphans(500 * time.Millisecond)
	return nil
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
