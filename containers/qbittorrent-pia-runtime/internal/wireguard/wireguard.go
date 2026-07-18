package wireguard

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

type KeyPair struct {
	Private string
	Public  string
}

type Registration struct {
	PeerIP     string
	ServerKey  string
	ServerIP   string
	ServerPort uint16
	DNSServers []string
}

func GenerateKeyPair() (KeyPair, error) {
	private, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return KeyPair{}, fmt.Errorf("generate ephemeral key: %w", err)
	}
	return KeyPair{
		Private: base64.StdEncoding.EncodeToString(private.Bytes()),
		Public:  base64.StdEncoding.EncodeToString(private.PublicKey().Bytes()),
	}, nil
}

func BuildConfig(keys KeyPair, endpointIP string, registration Registration) (string, error) {
	if err := validateKey(keys.Private); err != nil {
		return "", fmt.Errorf("private key: %w", err)
	}
	if err := validateKey(registration.ServerKey); err != nil {
		return "", fmt.Errorf("server key: %w", err)
	}
	if _, err := netip.ParsePrefix(registration.PeerIP); err != nil {
		return "", fmt.Errorf("peer address: %w", err)
	}
	endpoint, err := netip.ParseAddr(endpointIP)
	if err != nil {
		return "", fmt.Errorf("endpoint: %w", err)
	}
	if registration.ServerPort == 0 {
		return "", errors.New("server port is zero")
	}
	return "[Interface]\n" +
		"Address = " + registration.PeerIP + "\n" +
		"PrivateKey = " + keys.Private + "\n\n" +
		"[Peer]\n" +
		"PublicKey = " + registration.ServerKey + "\n" +
		"AllowedIPs = 0.0.0.0/0\n" +
		"Endpoint = " + netip.AddrPortFrom(endpoint, registration.ServerPort).String() + "\n" +
		"PersistentKeepalive = 25\n", nil
}

func ValidateConfig(value string) error {
	required := []string{"[Interface]", "Address = ", "PrivateKey = ", "[Peer]", "PublicKey = ", "AllowedIPs = 0.0.0.0/0", "Endpoint = ", "PersistentKeepalive = 25"}
	for _, item := range required {
		if !strings.Contains(value, item) {
			return fmt.Errorf("missing %q", item)
		}
	}
	for _, line := range strings.Split(value, "\n") {
		key, raw, ok := strings.Cut(line, " = ")
		if !ok {
			continue
		}
		switch key {
		case "PrivateKey", "PublicKey":
			if err := validateKey(raw); err != nil {
				return err
			}
		case "Endpoint":
			address, err := netip.ParseAddrPort(raw)
			if err != nil || address.Port() == 0 {
				return errors.New("invalid endpoint")
			}
		case "PersistentKeepalive":
			if raw != strconv.Itoa(25) {
				return errors.New("invalid keepalive")
			}
		}
	}
	return nil
}

func validateKey(value string) error {
	b, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(b) != 32 {
		return errors.New("invalid WireGuard key")
	}
	return nil
}
