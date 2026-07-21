package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestUsageIncludesSeparateExecChecks(t *testing.T) {
	err := run(nil)
	if err == nil || !strings.Contains(err.Error(), "healthcheck") || !strings.Contains(err.Error(), "readycheck") {
		t.Fatalf("usage error=%v", err)
	}
}

func TestExecProbeEndpointsAreDistinctAndLocal(t *testing.T) {
	if liveProbeURL != "http://127.0.0.1:8001/live" || readyProbeURL != "http://127.0.0.1:8001/ready" {
		t.Fatalf("live=%q ready=%q", liveProbeURL, readyProbeURL)
	}
	for _, endpoint := range []string{liveProbeURL, readyProbeURL} {
		if err := validateProbeEndpoint(endpoint); err != nil {
			t.Fatal(err)
		}
	}
	for _, endpoint := range []string{"https://127.0.0.1:8001/ready", "http://localhost:8001/ready", "http://127.0.0.1:8002/ready", "http://127.0.0.1:8001/other", "http://127.0.0.1:8001/ready?redirect=1"} {
		if err := validateProbeEndpoint(endpoint); err == nil {
			t.Fatalf("accepted non-local probe endpoint %q", endpoint)
		}
	}
}

func TestProbeStatusAndRedirectFailure(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
		defer server.Close()
		if err := probeResponse(&http.Client{Timeout: probeTimeout}, server.URL); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("failure", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
		}))
		defer server.Close()
		if err := probeResponse(&http.Client{Timeout: probeTimeout}, server.URL); err == nil {
			t.Fatal("accepted failed probe")
		}
	})
	t.Run("redirect is not followed", func(t *testing.T) {
		var externalHits atomic.Int32
		target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { externalHits.Add(1) }))
		defer target.Close()
		server := httptest.NewServer(http.RedirectHandler(target.URL, http.StatusFound))
		defer server.Close()
		client := &http.Client{Timeout: probeTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		if err := probeResponse(client, server.URL); err == nil || externalHits.Load() != 0 {
			t.Fatalf("redirect error=%v external hits=%d", err, externalHits.Load())
		}
	})
	t.Run("bounded timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(100 * time.Millisecond)
			w.WriteHeader(http.StatusNoContent)
		}))
		defer server.Close()
		started := time.Now()
		if err := probeResponse(&http.Client{Timeout: 20 * time.Millisecond}, server.URL); err == nil || time.Since(started) > 500*time.Millisecond {
			t.Fatalf("timeout error=%v duration=%s", err, time.Since(started))
		}
	})
}
