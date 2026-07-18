package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/andycooney/home-ops/containers/qbittorrent-pia-runtime/internal/api"
	"github.com/andycooney/home-ops/containers/qbittorrent-pia-runtime/internal/config"
	"github.com/andycooney/home-ops/containers/qbittorrent-pia-runtime/internal/firewall"
	"github.com/andycooney/home-ops/containers/qbittorrent-pia-runtime/internal/health"
	"github.com/andycooney/home-ops/containers/qbittorrent-pia-runtime/internal/session"
	"github.com/andycooney/home-ops/containers/qbittorrent-pia-runtime/internal/supervisor"
	"github.com/andycooney/home-ops/containers/qbittorrent-pia-runtime/internal/wireguard"
)

var version = "dev"
var revision = "unknown"
var created = "unknown"

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Printf("pia-runtime: %s", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: pia-runtime <firewall-init|supervise|self-test|healthcheck>")
	}
	switch args[0] {
	case "firewall-init":
		return firewallInit()
	case "supervise":
		return supervise()
	case "self-test":
		return selfTest()
	case "healthcheck":
		return probe("http://127.0.0.1:8001/live")
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func firewallInit() error {
	cfg, err := config.LoadFirewall()
	if err != nil {
		return err
	}
	manager := &firewall.Manager{Config: firewall.Config{AllowedSubnets: cfg.AllowedSubnets, ApplicationUID: cfg.ApplicationUID, ServicePort: cfg.ServicePort, Interface: cfg.Interface}}
	return manager.Init(context.Background())
}

func supervise() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := session.RequireTmpfs(cfg.RuntimeDir); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	firewallManager := &firewall.Manager{Config: firewall.Config{AllowedSubnets: cfg.AllowedSubnets, ApplicationUID: cfg.ApplicationUID, ServicePort: cfg.ServicePort, Interface: cfg.Interface}}
	if err := firewallManager.Init(ctx); err != nil {
		return err
	}
	status := health.NewStatus()
	server := &health.Server{Address: cfg.ListenAddress, Status: status, MaxSilence: 2 * time.Minute}
	if err := server.Start(); err != nil {
		return err
	}
	defer server.Shutdown(context.Background())
	apiClient := &api.Client{ServerListURL: cfg.ServerListURL, TokenURL: cfg.TokenURL, CACertPath: cfg.CACertPath, ProbeTimeout: cfg.ProbeTimeout}
	publisher := &session.Publisher{Root: cfg.RuntimeDir, ReaderGID: cfg.ReaderGID}
	verifier := &health.Verifier{Interface: cfg.Interface, HTTPSURL: cfg.PublicIPURL, Timeout: cfg.ProbeTimeout}
	s := &supervisor.Supervisor{Config: cfg, API: apiClient, Firewall: firewallManager, Publisher: publisher, Verifier: verifier, Process: supervisor.OSProcess{}, Status: status, Logger: log.New(os.Stdout, "pia-runtime ", log.LstdFlags|log.LUTC)}
	return s.Run(ctx)
}

func selfTest() error {
	if version == "" || revision == "" || created == "" {
		return errors.New("build metadata is empty")
	}
	keys, err := wireguard.GenerateKeyPair()
	if err != nil {
		return err
	}
	registration := wireguard.Registration{PeerIP: "10.0.0.2/32", ServerKey: keys.Public, ServerIP: "10.0.0.1", ServerPort: 1337, DNSServers: []string{"10.0.0.1"}}
	conf, err := wireguard.BuildConfig(keys, "192.0.2.10", registration)
	if err != nil {
		return err
	}
	if err := wireguard.ValidateConfig(conf); err != nil {
		return err
	}
	fixture := `{"groups":{"wg":[{"name":"wireguard"}]},"regions":[{"id":"ca","name":"Canada","country":"CA","port_forward":true,"offline":false,"servers":{"wg":[{"ip":"192.0.2.10","cn":"example.invalid"}]}}]}`
	if _, err := api.ParseServerList(strings.NewReader(fixture)); err != nil {
		return err
	}
	if os.Getenv("PIA_RUNTIME_IMAGE_SELF_TEST") == "1" {
		if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
			return errors.New("image self-test requires linux/amd64")
		}
		for _, path := range []string{"/gluetun-entrypoint", "/usr/sbin/iptables", "/usr/sbin/ip6tables", "/usr/sbin/iptables-restore", "/usr/sbin/ip6tables-restore", "/usr/sbin/iptables-legacy", "/usr/sbin/ip6tables-legacy", "/usr/sbin/iptables-legacy-restore", "/usr/sbin/ip6tables-legacy-restore", "/etc/ssl/certs/ca-certificates.crt", "/usr/local/share/pia/ca.rsa.4096.crt"} {
			if info, err := os.Stat(filepath.Clean(path)); err != nil || info.IsDir() {
				return fmt.Errorf("required base-image file missing: %s", path)
			}
		}
		caBytes, err := os.ReadFile("/usr/local/share/pia/ca.rsa.4096.crt")
		if err != nil {
			return err
		}
		if fmt.Sprintf("%x", sha256.Sum256(caBytes)) != "32e9b1d1433ea97614f2a14c6e358e3f57c0570cc9f6b2ee812699ba696c66ab" {
			return errors.New("vendored PIA CA integrity check failed")
		}
	}
	fmt.Printf("pia-runtime self-test ok version=%s revision=%s created=%s\n", version, revision, created)
	return nil
}

func probe(endpoint string) error {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(endpoint)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("probe status %d", resp.StatusCode)
	}
	return nil
}
