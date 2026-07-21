package health

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestLivenessReadinessSeparationAndRecoverableOutage(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	status := NewStatus()
	server := &Server{Address: address, Status: status, MaxSilence: time.Minute}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer server.Shutdown(context.Background())
	waitHTTP(t, "http://"+address+"/live")
	assertStatus(t, "http://"+address+"/live", http.StatusNoContent)
	assertStatus(t, "http://"+address+"/ready", http.StatusServiceUnavailable)
	status.Set("BACKOFF", false)
	assertStatus(t, "http://"+address+"/live", http.StatusNoContent)
	assertStatus(t, "http://"+address+"/ready", http.StatusServiceUnavailable)
	status.Set("HEALTHY", true)
	assertStatus(t, "http://"+address+"/ready", http.StatusNoContent)
	status.Fail()
	assertStatus(t, "http://"+address+"/live", http.StatusServiceUnavailable)
}

func TestPublicIPParsingDoesNotRequireLoggingValue(t *testing.T) {
	connectionClose := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		connectionClose = request.Close
		_, _ = w.Write([]byte("fl=1\nip=192.0.2.44\nts=1\n"))
	}))
	defer server.Close()
	ip, err := PreTunnelPublicIP(context.Background(), server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if ip.String() != "192.0.2.44" {
		t.Fatalf("ip=%v", ip)
	}
	if !connectionClose {
		t.Fatal("bootstrap public-IP request permitted connection reuse")
	}
}

func TestInvalidPublicIPResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("secret-response-body")) }))
	defer server.Close()
	if _, err := PreTunnelPublicIP(context.Background(), server.URL, time.Second); err == nil {
		t.Fatal("invalid response accepted")
	}
}

func waitHTTP(t *testing.T, endpoint string) {
	t.Helper()
	for i := 0; i < 50; i++ {
		resp, err := http.Get(endpoint)
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("HTTP server did not start")
}
func assertStatus(t *testing.T, endpoint string, want int) {
	t.Helper()
	resp, err := http.Get(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != want {
		t.Fatalf("%s status=%d want=%d", endpoint, resp.StatusCode, want)
	}
}
