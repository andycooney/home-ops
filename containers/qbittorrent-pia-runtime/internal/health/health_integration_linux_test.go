//go:build linux

package health

import (
	"context"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"syscall"
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
			questionEnd := 12
			for questionEnd < n && buffer[questionEnd] != 0 {
				questionEnd += int(buffer[questionEnd]) + 1
			}
			questionEnd++
			if questionEnd+4 > n {
				continue
			}
			queryType := append([]byte(nil), buffer[questionEnd:questionEnd+2]...)
			questionEnd += 4
			response := append([]byte(nil), buffer[:questionEnd]...)
			response[2], response[3] = 0x81, 0x80
			response[6], response[7] = 0, 1
			response[8], response[9], response[10], response[11] = 0, 0, 0, 0
			answer := []byte{127, 0, 0, 1}
			if queryType[0] == 0 && queryType[1] == 28 {
				answer = make([]byte, 16)
				answer[15] = 1
			}
			response = append(response, 0xc0, 0x0c, queryType[0], queryType[1], 0, 1, 0, 0, 0, 0, byte(len(answer)>>8), byte(len(answer)))
			response = append(response, answer...)
			_, _ = dns.WriteTo(response, address)
		}
	}()
	https := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ip=192.0.2.55\n")) }))
	defer https.Close()
	pool := x509.NewCertPool()
	pool.AddCert(https.Certificate())
	var controlCalls atomic.Int32
	control := func(string, string, syscall.RawConn) error { controlCalls.Add(1); return nil }
	verifier := Verifier{Interface: "lo", DNSAddress: dns.LocalAddr().String(), DNSName: "health.test", HTTPSURL: https.URL, Timeout: 3 * time.Second, RootCAs: pool, Control: control}
	result, err := verifier.Verify(context.Background(), netip.MustParseAddr("198.51.100.1"))
	if err != nil {
		t.Fatal(err)
	}
	if result.PublicIP.String() != "192.0.2.55" || result.After.RX <= result.Before.RX || result.After.TX <= result.Before.TX {
		t.Fatalf("result=%+v", result)
	}
	if controlCalls.Load() < 2 {
		t.Fatalf("bind control calls=%d", controlCalls.Load())
	}
}
