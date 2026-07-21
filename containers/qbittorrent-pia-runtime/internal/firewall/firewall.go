package firewall

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
)

type State string

const (
	Bootstrap State = "BOOTSTRAP"
	Selected  State = "SELECTED"
	Verifying State = "VERIFYING"
	Healthy   State = "HEALTHY"
	Locked    State = "LOCKED"
)

type Config struct {
	AllowedSubnets []netip.Prefix
	ApplicationUID int
	ServicePort    uint16
	Interface      string
}
type Endpoint struct {
	IP   netip.Addr
	Port uint16
}
type Runner interface {
	Run(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error)
}
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args []string, stdin []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = bytes.NewReader(stdin)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("firewall command %s failed", name)
	}
	return output, nil
}

type Manager struct {
	Config Config
	Runner Runner
}

func (m Manager) Init(ctx context.Context) error { return m.Apply(ctx, Bootstrap, Endpoint{}) }

func (m Manager) Apply(ctx context.Context, state State, endpoint Endpoint) error {
	if m.Runner == nil {
		m.Runner = ExecRunner{}
	}
	for _, family := range []struct {
		restore, command string
		ipv6             bool
	}{{"iptables-restore", "iptables", false}, {"ip6tables-restore", "ip6tables", true}} {
		tx, err := Transaction(m.Config, state, endpoint, family.ipv6)
		if err != nil {
			return err
		}
		if _, err := m.Runner.Run(ctx, family.restore, []string{"--noflush", "--wait", "5"}, []byte(tx)); err != nil {
			return err
		}
		for _, hook := range []string{"INPUT", "OUTPUT", "FORWARD"} {
			chain := chainName(hook)
			rules, err := m.Runner.Run(ctx, family.command, []string{"--wait", "5", "-S", hook}, nil)
			if err != nil {
				return err
			}
			if !hookIsFirst(string(rules), hook, chain) {
				if _, err := m.Runner.Run(ctx, family.command, []string{"--wait", "5", "-I", hook, "1", "-j", chain}, nil); err != nil {
					return err
				}
			}
		}
		output, err := m.Runner.Run(ctx, family.command, []string{"--wait", "5", "-S"}, nil)
		if err != nil {
			return err
		}
		if err := Verify(string(output)); err != nil {
			return err
		}
	}
	return nil
}

func Transaction(cfg Config, state State, endpoint Endpoint, ipv6 bool) (string, error) {
	if cfg.ApplicationUID <= 0 || cfg.ServicePort == 0 || cfg.Interface == "" {
		return "", errors.New("invalid firewall configuration")
	}
	if state != Bootstrap && state != Selected && state != Verifying && state != Healthy && state != Locked {
		return "", errors.New("invalid firewall state")
	}
	if (state == Selected || state == Verifying || state == Healthy) && (!endpoint.IP.IsValid() || endpoint.Port == 0) {
		return "", errors.New("active state requires endpoint")
	}
	var b strings.Builder
	b.WriteString("*filter\n:INPUT DROP [0:0]\n:OUTPUT DROP [0:0]\n:FORWARD DROP [0:0]\n")
	for _, chain := range []string{"PIA_RUNTIME_INPUT", "PIA_RUNTIME_OUTPUT", "PIA_RUNTIME_FORWARD"} {
		fmt.Fprintf(&b, ":%s - [0:0]\n-F %s\n", chain, chain)
	}
	b.WriteString("-A PIA_RUNTIME_INPUT -i lo -j ACCEPT\n-A PIA_RUNTIME_INPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT\n")
	b.WriteString("-A PIA_RUNTIME_OUTPUT -o lo -j ACCEPT\n")
	for _, subnet := range cfg.AllowedSubnets {
		if subnet.Addr().Is6() != ipv6 {
			continue
		}
		fmt.Fprintf(&b, "-A PIA_RUNTIME_OUTPUT -m owner --uid-owner %d ! -o %s -p tcp --sport %d -d %s -m conntrack --ctstate ESTABLISHED -j ACCEPT\n", cfg.ApplicationUID, cfg.Interface, cfg.ServicePort, subnet)
	}
	fmt.Fprintf(&b, "-A PIA_RUNTIME_OUTPUT -m owner --uid-owner %d ! -o %s -j DROP\n", cfg.ApplicationUID, cfg.Interface)
	b.WriteString("-A PIA_RUNTIME_OUTPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT\n")
	for _, subnet := range cfg.AllowedSubnets {
		if subnet.Addr().Is6() != ipv6 {
			continue
		}
		fmt.Fprintf(&b, "-A PIA_RUNTIME_INPUT -s %s -p tcp --dport %d -j ACCEPT\n", subnet, cfg.ServicePort)
		fmt.Fprintf(&b, "-A PIA_RUNTIME_OUTPUT -d %s -j ACCEPT\n", subnet)
	}
	if state == Bootstrap {
		fmt.Fprintf(&b, "-A PIA_RUNTIME_OUTPUT -m owner --uid-owner 0 -p udp --dport 53 -j ACCEPT\n-A PIA_RUNTIME_OUTPUT -m owner --uid-owner 0 -p tcp -m multiport --dports 53,443 -j ACCEPT\n")
	}
	if state == Selected || state == Verifying || state == Healthy {
		protocol := "udp"
		if state == Selected {
			protocol = "tcp"
		}
		if endpoint.IP.Is6() == ipv6 {
			fmt.Fprintf(&b, "-A PIA_RUNTIME_OUTPUT -p %s -d %s --dport %d -j ACCEPT\n", protocol, endpoint.IP, endpoint.Port)
		}
	}
	if state == Verifying || state == Healthy {
		fmt.Fprintf(&b, "-A PIA_RUNTIME_OUTPUT -m owner --uid-owner 0 -o %s -j ACCEPT\n", cfg.Interface)
	}
	if state == Healthy {
		fmt.Fprintf(&b, "-A PIA_RUNTIME_OUTPUT -m owner --uid-owner %d -o %s -j ACCEPT\n", cfg.ApplicationUID, cfg.Interface)
	}
	b.WriteString("-A PIA_RUNTIME_INPUT -j DROP\n-A PIA_RUNTIME_OUTPUT -j DROP\n-A PIA_RUNTIME_FORWARD -j DROP\nCOMMIT\n")
	return b.String(), nil
}

func Verify(rules string) error {
	required := []string{"-P INPUT DROP", "-P OUTPUT DROP", "-P FORWARD DROP", "-N PIA_RUNTIME_INPUT", "-N PIA_RUNTIME_OUTPUT", "-N PIA_RUNTIME_FORWARD", "-A INPUT -j PIA_RUNTIME_INPUT", "-A OUTPUT -j PIA_RUNTIME_OUTPUT", "-A FORWARD -j PIA_RUNTIME_FORWARD"}
	for _, item := range required {
		if !strings.Contains(rules, item) {
			return fmt.Errorf("firewall verification missing %s", strconv.Quote(item))
		}
	}
	return nil
}
func chainName(hook string) string { return "PIA_RUNTIME_" + hook }

func hookIsFirst(rules, hook, chain string) bool {
	want := "-A " + hook + " -j " + chain
	for _, line := range strings.Split(rules, "\n") {
		if strings.HasPrefix(line, "-A "+hook+" ") {
			return line == want
		}
	}
	return false
}
