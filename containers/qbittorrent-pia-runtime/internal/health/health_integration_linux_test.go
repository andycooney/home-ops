//go:build linux

package health

import (
	"context"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestOfflineTunnelBoundDNSHTTPSAndActivity(t *testing.T) {
	dns, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer dns.Close()
	go func() {
		buffer := make([]byte, 1500)
		for {
			n, address, err := dns.ReadFrom(buffer)
			if err != nil {
				return
			}
			if n < 12 {
				continue
			}
			response := append([]byte(nil), buffer[:n]...)
			response[2], response[3] = 0x81, 0x80
			response[6], response[7] = 0, 1
			response = append(response, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 0, 0, 4, 127, 0, 0, 1)
			_, _ = dns.WriteTo(response, address)
		}
	}()
	https := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ip=192.0.2.55\n")) }))
	defer https.Close()
	pool := x509.NewCertPool()
	pool.AddCert(https.Certificate())
	verifier := Verifier{Interface: "lo", DNSAddress: dns.LocalAddr().String(), DNSName: "health.test", HTTPSURL: https.URL, Timeout: 3 * time.Second, RootCAs: pool}
	result, err := verifier.Verify(context.Background(), netip.MustParseAddr("198.51.100.1"))
	if err != nil {
		t.Fatal(err)
	}
	if result.PublicIP.String() != "192.0.2.55" || result.After.RX <= result.Before.RX || result.After.TX <= result.Before.TX {
		t.Fatalf("result=%+v", result)
	}
}
