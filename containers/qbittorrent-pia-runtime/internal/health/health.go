package health

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type Status struct {
	state atomic.Value
	ready atomic.Bool
	live  atomic.Bool
	last  atomic.Int64
}

func NewStatus() *Status {
	s := &Status{}
	s.state.Store("BOOTSTRAP")
	s.live.Store(true)
	s.Beat()
	return s
}
func (s *Status) Set(state string, ready bool) { s.state.Store(state); s.ready.Store(ready); s.Beat() }
func (s *Status) Beat()                        { s.last.Store(time.Now().UnixNano()) }
func (s *Status) Fail()                        { s.live.Store(false); s.ready.Store(false) }
func (s *Status) State() string {
	v := s.state.Load()
	if v == nil {
		return "BOOTSTRAP"
	}
	return v.(string)
}
func (s *Status) Ready() bool { return s.ready.Load() }
func (s *Status) Live(maxSilence time.Duration) bool {
	return s.live.Load() && time.Since(time.Unix(0, s.last.Load())) <= maxSilence
}

type Server struct {
	Address    string
	Status     *Status
	MaxSilence time.Duration
	server     *http.Server
}

func (s *Server) Start() error {
	if s.Status == nil {
		return errors.New("status is required")
	}
	if s.MaxSilence == 0 {
		s.MaxSilence = 2 * time.Minute
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/live", func(w http.ResponseWriter, _ *http.Request) {
		if !s.Status.Live(s.MaxSilence) {
			http.Error(w, "not live", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) {
		if !s.Status.Ready() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	s.server = &http.Server{Addr: s.Address, Handler: mux, ReadHeaderTimeout: 2 * time.Second, IdleTimeout: 30 * time.Second}
	listener, err := net.Listen("tcp", s.Address)
	if err != nil {
		return err
	}
	s.Address = listener.Addr().String()
	go func() {
		if err := s.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.Status.Fail()
		}
	}()
	return nil
}
func (s *Server) Shutdown(ctx context.Context) error {
	if s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

type Counters struct{ RX, TX uint64 }
type Result struct {
	PublicIP      netip.Addr
	Before, After Counters
}
type Verifier struct {
	Interface, DNSAddress, HTTPSURL string
	DNSName                         string
	Timeout                         time.Duration
	RootCAs                         *x509.CertPool
}

func (v Verifier) Verify(ctx context.Context, preTunnelIP netip.Addr) (Result, error) {
	if v.Interface == "" || v.DNSAddress == "" || v.HTTPSURL == "" {
		return Result{}, errors.New("incomplete health configuration")
	}
	if v.Timeout == 0 {
		v.Timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, v.Timeout)
	defer cancel()
	before, err := ReadCounters(v.Interface)
	if err != nil {
		return Result{}, errors.New("tunnel counters unavailable")
	}
	resolver := &net.Resolver{PreferGo: true, StrictErrors: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		d := net.Dialer{Timeout: v.Timeout, Control: bindToDevice(v.Interface)}
		return d.DialContext(ctx, network, dnsEndpoint(v.DNSAddress))
	}}
	dnsName := v.DNSName
	if dnsName == "" {
		dnsName = hostFromURL(v.HTTPSURL)
	}
	if _, err := resolver.LookupHost(ctx, dnsName); err != nil {
		return Result{}, errors.New("tunneled DNS failed")
	}
	dialer := &net.Dialer{Timeout: v.Timeout, Control: bindToDevice(v.Interface)}
	transport := &http.Transport{DialContext: dialer.DialContext, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: v.RootCAs}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: v.Timeout}
	ip, err := publicIP(ctx, client, v.HTTPSURL)
	if err != nil {
		return Result{}, errors.New("tunneled HTTPS failed")
	}
	if !preTunnelIP.IsValid() || ip == preTunnelIP {
		return Result{}, errors.New("tunneled public IP was not changed")
	}
	after, err := ReadCounters(v.Interface)
	if err != nil {
		return Result{}, errors.New("tunnel counters unavailable")
	}
	if after.RX <= before.RX || after.TX <= before.TX {
		return Result{}, errors.New("no meaningful tunnel activity")
	}
	return Result{PublicIP: ip, Before: before, After: after}, nil
}

func PreTunnelPublicIP(ctx context.Context, endpoint string, timeout time.Duration) (netip.Addr, error) {
	client := &http.Client{Timeout: timeout}
	return publicIP(ctx, client, endpoint)
}

func publicIP(ctx context.Context, client *http.Client, endpoint string) (netip.Addr, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return netip.Addr{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return netip.Addr{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return netip.Addr{}, fmt.Errorf("public IP status %d", resp.StatusCode)
	}
	reader := bufio.NewReader(io.LimitReader(resp.Body, 64*1024))
	body, err := io.ReadAll(reader)
	if err != nil {
		return netip.Addr{}, err
	}
	value := strings.TrimSpace(string(body))
	for _, line := range strings.Split(value, "\n") {
		if strings.HasPrefix(line, "ip=") {
			value = strings.TrimSpace(strings.TrimPrefix(line, "ip="))
			break
		}
	}
	ip, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Addr{}, errors.New("invalid public IP response")
	}
	return ip.Unmap(), nil
}

func ReadCounters(interfaceName string) (Counters, error) {
	if _, err := net.InterfaceByName(interfaceName); err != nil {
		return Counters{}, err
	}
	read := func(name string) (uint64, error) {
		b, err := os.ReadFile("/sys/class/net/" + interfaceName + "/statistics/" + name + "_bytes")
		if err != nil {
			return 0, err
		}
		return strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	}
	rx, err := read("rx")
	if err != nil {
		return Counters{}, err
	}
	tx, err := read("tx")
	if err != nil {
		return Counters{}, err
	}
	return Counters{RX: rx, TX: tx}, nil
}
func hostFromURL(value string) string {
	value = strings.TrimPrefix(value, "https://")
	value = strings.Split(value, "/")[0]
	host, _, err := net.SplitHostPort(value)
	if err == nil {
		return host
	}
	return value
}

func dnsEndpoint(value string) string {
	if _, _, err := net.SplitHostPort(value); err == nil {
		return value
	}
	return net.JoinHostPort(value, "53")
}
