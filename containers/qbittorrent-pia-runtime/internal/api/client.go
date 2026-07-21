package api

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/andycooney/home-ops/containers/qbittorrent-pia-runtime/internal/wireguard"
)

const maxResponseBytes = 8 << 20

type ServerList struct {
	Groups  map[string][]Group `json:"groups"`
	Regions []Region           `json:"regions"`
}
type Group struct {
	Name string `json:"name"`
}
type Region struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Country     string  `json:"country"`
	PortForward *bool   `json:"port_forward"`
	Offline     *bool   `json:"offline"`
	Servers     Servers `json:"servers"`
}
type Servers struct {
	WG []Endpoint `json:"wg"`
}
type Endpoint struct {
	IP       string `json:"ip"`
	Hostname string `json:"cn"`
}
type Candidate struct {
	RegionID, RegionName, Country, IP, Hostname string
	Port                                        uint16
}

type Client struct {
	HTTP          *http.Client
	ServerListURL string
	TokenURL      string
	CACertPath    string
	ProbeTimeout  time.Duration
}

type AuthError struct{ Err error }

func (e *AuthError) Error() string    { return "authentication failed" }
func (e *AuthError) Unwrap() error    { return e.Err }
func IsAuthentication(err error) bool { var target *AuthError; return errors.As(err, &target) }

func (c *Client) FetchServerList(ctx context.Context) (ServerList, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.ServerListURL, nil)
	if err != nil {
		return ServerList{}, err
	}
	resp, err := c.client().Do(req)
	if err != nil {
		return ServerList{}, fmt.Errorf("server metadata request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ServerList{}, fmt.Errorf("server metadata status %d", resp.StatusCode)
	}
	return ParseServerList(io.LimitReader(resp.Body, maxResponseBytes))
}

func ParseServerList(r io.Reader) (ServerList, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), maxResponseBytes)
	if !scanner.Scan() {
		return ServerList{}, errors.New("empty server metadata")
	}
	var list ServerList
	decoder := json.NewDecoder(strings.NewReader(scanner.Text()))
	if err := decoder.Decode(&list); err != nil {
		return ServerList{}, errors.New("malformed server metadata")
	}
	if list.Groups == nil || len(list.Groups["wg"]) == 0 || len(list.Regions) == 0 {
		return ServerList{}, errors.New("unknown or incomplete server metadata schema")
	}
	for _, region := range list.Regions {
		if region.ID == "" || region.Name == "" || len(region.Country) != 2 || region.PortForward == nil || region.Offline == nil || region.Servers.WG == nil {
			return ServerList{}, errors.New("unknown or incomplete server metadata schema")
		}
		for _, endpoint := range region.Servers.WG {
			if endpoint.IP == "" || endpoint.Hostname == "" {
				return ServerList{}, errors.New("incomplete WireGuard endpoint")
			}
			if _, err := netip.ParseAddr(endpoint.IP); err != nil {
				return ServerList{}, errors.New("invalid WireGuard endpoint")
			}
		}
	}
	return list, nil
}

func SelectCandidates(list ServerList, preferred []string, cooldown map[string]time.Time, now time.Time, max int) []Candidate {
	preference := make(map[string]int, len(preferred))
	for i, id := range preferred {
		preference[strings.ToLower(id)] = i
	}
	var candidates []Candidate
	for _, region := range list.Regions {
		if strings.EqualFold(region.Country, "US") || region.PortForward == nil || !*region.PortForward || region.Offline == nil || *region.Offline {
			continue
		}
		for _, endpoint := range region.Servers.WG {
			if until, found := cooldown[endpoint.IP]; found && now.Before(until) {
				continue
			}
			candidates = append(candidates, Candidate{RegionID: region.ID, RegionName: region.Name, Country: region.Country, IP: endpoint.IP, Hostname: endpoint.Hostname, Port: 1337})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		pi, iok := preference[strings.ToLower(candidates[i].RegionID)]
		pj, jok := preference[strings.ToLower(candidates[j].RegionID)]
		if iok != jok {
			return iok
		}
		if iok && pi != pj {
			return pi < pj
		}
		if candidates[i].RegionID != candidates[j].RegionID {
			return candidates[i].RegionID < candidates[j].RegionID
		}
		return candidates[i].IP < candidates[j].IP
	})
	seenEndpoints := make(map[string]struct{}, len(candidates))
	distinct := candidates[:0]
	for _, candidate := range candidates {
		if _, seen := seenEndpoints[candidate.IP]; seen {
			continue
		}
		seenEndpoints[candidate.IP] = struct{}{}
		distinct = append(distinct, candidate)
	}
	candidates = distinct
	if max > 0 && len(candidates) > max {
		candidates = candidates[:max]
	}
	return candidates
}

func (c *Client) Probe(ctx context.Context, candidate Candidate) error {
	ctx, cancel := context.WithTimeout(ctx, c.ProbeTimeout)
	defer cancel()
	roots, err := c.roots()
	if err != nil {
		return err
	}
	dialer := net.Dialer{Timeout: c.ProbeTimeout}
	raw, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(candidate.IP, strconv.Itoa(int(candidate.Port))))
	if err != nil {
		return errors.New("candidate TCP probe failed")
	}
	conn := tls.Client(raw, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: candidate.Hostname, RootCAs: roots})
	if err := conn.HandshakeContext(ctx); err != nil {
		raw.Close()
		return errors.New("candidate TLS probe failed")
	}
	return conn.Close()
}

func (c *Client) Token(ctx context.Context, username, password string) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("username", username); err != nil {
		return "", err
	}
	if err := writer.WriteField("password", password); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.TokenURL, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := c.client().Do(req)
	if err != nil {
		return "", errors.New("token request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", &AuthError{Err: errors.New("credentials rejected")}
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token status %d", resp.StatusCode)
	}
	var parsed struct {
		Token  string `json:"token"`
		Status string `json:"status"`
	}
	if err := decodeJSON(resp.Body, &parsed); err != nil {
		return "", errors.New("invalid token response")
	}
	if parsed.Token == "" {
		return "", &AuthError{Err: errors.New("token absent")}
	}
	return parsed.Token, nil
}

func (c *Client) Register(ctx context.Context, candidate Candidate, token, publicKey string) (wireguard.Registration, error) {
	values := url.Values{"pt": {token}, "pubkey": {publicKey}}
	endpoint := "https://" + net.JoinHostPort(candidate.Hostname, strconv.Itoa(int(candidate.Port))) + "/addKey?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return wireguard.Registration{}, err
	}
	dialer := net.Dialer{Timeout: c.ProbeTimeout}
	roots, err := c.roots()
	if err != nil {
		return wireguard.Registration{}, err
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, ServerName: candidate.Hostname, RootCAs: roots}, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP, strconv.Itoa(int(candidate.Port))))
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: c.ProbeTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return wireguard.Registration{}, errors.New("WireGuard registration failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return wireguard.Registration{}, &AuthError{Err: errors.New("token rejected")}
	}
	if resp.StatusCode != http.StatusOK {
		return wireguard.Registration{}, fmt.Errorf("WireGuard registration status %d", resp.StatusCode)
	}
	var raw struct {
		Status     string   `json:"status"`
		ServerKey  string   `json:"server_key"`
		ServerIP   string   `json:"server_ip"`
		PeerIP     string   `json:"peer_ip"`
		ServerPort int      `json:"server_port"`
		DNSServers []string `json:"dns_servers"`
	}
	if err := decodeJSON(resp.Body, &raw); err != nil {
		return wireguard.Registration{}, errors.New("invalid WireGuard registration response")
	}
	if raw.Status != "OK" || raw.ServerKey == "" || raw.ServerIP == "" || raw.PeerIP == "" || raw.ServerPort < 1 || raw.ServerPort > 65535 || len(raw.DNSServers) == 0 {
		return wireguard.Registration{}, errors.New("incomplete WireGuard registration response")
	}
	if _, err := netip.ParseAddr(raw.ServerIP); err != nil {
		return wireguard.Registration{}, errors.New("invalid WireGuard gateway")
	}
	if !strings.Contains(raw.PeerIP, "/") {
		raw.PeerIP += "/32"
	}
	if _, err := netip.ParsePrefix(raw.PeerIP); err != nil {
		return wireguard.Registration{}, errors.New("invalid WireGuard peer address")
	}
	for _, dns := range raw.DNSServers {
		if _, err := netip.ParseAddr(dns); err != nil {
			return wireguard.Registration{}, errors.New("invalid DNS server")
		}
	}
	return wireguard.Registration{PeerIP: raw.PeerIP, ServerKey: raw.ServerKey, ServerIP: raw.ServerIP, ServerPort: uint16(raw.ServerPort), DNSServers: raw.DNSServers}, nil
}

func (c *Client) roots() (*x509.CertPool, error) {
	pemBytes, err := os.ReadFile(c.CACertPath)
	if err != nil {
		return nil, errors.New("PIA CA certificate unavailable")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, errors.New("PIA CA certificate invalid")
	}
	return pool, nil
}
func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}
func decodeJSON(r io.Reader, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r, maxResponseBytes))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
