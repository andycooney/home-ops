package firewall

import (
	"context"
	"net/netip"
	"strings"
	"sync"
	"testing"
)

func testConfig() Config {
	return Config{AllowedSubnets: []netip.Prefix{netip.MustParsePrefix("10.42.0.0/16"), netip.MustParsePrefix("fd00:42::/64")}, ApplicationUID: 1000, PFHelperUID: 65532, ServicePort: 8080, Interface: "tun0"}
}

func TestCompleteTransactionsAndNoUIDDefaultAllowance(t *testing.T) {
	endpoint := Endpoint{IP: netip.MustParseAddr("192.0.2.10"), Port: 1337, PFGateway: netip.MustParseAddr("192.0.2.20")}
	appAllow := "-A PIA_RUNTIME_OUTPUT -m owner --uid-owner 1000 -o tun0 -j ACCEPT"
	appDrop := "-A PIA_RUNTIME_OUTPUT -m owner --uid-owner 1000 -j DROP"
	established := "-A PIA_RUNTIME_OUTPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT"
	for _, state := range []State{Bootstrap, Selected, Verifying, Healthy, Locked} {
		for _, ipv6 := range []bool{false, true} {
			tx, err := Transaction(testConfig(), state, endpointFor(state, endpoint), ipv6)
			if err != nil {
				t.Fatalf("%s ipv6=%v: %v", state, ipv6, err)
			}
			for _, policy := range []string{":INPUT DROP", ":OUTPUT DROP", ":FORWARD DROP", "-A PIA_RUNTIME_OUTPUT -j DROP", "COMMIT"} {
				if !strings.Contains(tx, policy) {
					t.Fatalf("%s missing %q", state, policy)
				}
			}
			for _, line := range strings.Split(tx, "\n") {
				if strings.Contains(line, "--uid-owner 1000") && strings.Contains(line, "-j ACCEPT") && !strings.Contains(line, "-o tun0") {
					t.Fatalf("transient/default UID allowance in %s: %s", state, line)
				}
			}
			if !strings.Contains(tx, appDrop) {
				t.Fatalf("%s lacks unconditional application UID block", state)
			}
			if strings.Index(tx, appDrop) > strings.Index(tx, established) {
				t.Fatalf("%s evaluates established output before UID block", state)
			}
			if state == Healthy {
				if allowIndex, dropIndex := strings.Index(tx, appAllow), strings.Index(tx, appDrop); allowIndex < 0 || allowIndex > dropIndex {
					t.Fatalf("%s does not allow new and established application tunnel traffic before the UID drop", state)
				}
			} else if strings.Contains(tx, appAllow) {
				t.Fatalf("%s permits new or established application tunnel traffic", state)
			}
			for _, laterAllowance := range []string{"-A PIA_RUNTIME_OUTPUT -d 10.42.0.0/16 -j ACCEPT", "-A PIA_RUNTIME_OUTPUT -p udp -d 192.0.2.10 --dport 1337 -j ACCEPT"} {
				if index := strings.Index(tx, laterAllowance); index >= 0 && strings.Index(tx, appDrop) > index {
					t.Fatalf("%s evaluates %q before the application UID drop", state, laterAllowance)
				}
			}
		}
	}
}

func TestApplicationUIDTunnelAndEstablishedTrafficIsHealthyOnly(t *testing.T) {
	endpoint := Endpoint{IP: netip.MustParseAddr("192.0.2.10"), Port: 1337, PFGateway: netip.MustParseAddr("192.0.2.20")}
	reply := "-A PIA_RUNTIME_OUTPUT -m owner --uid-owner 1000 ! -o tun0 -p tcp --sport 8080 -d 10.42.0.0/16 -m conntrack --ctstate ESTABLISHED -j ACCEPT"
	tunnel := "-A PIA_RUNTIME_OUTPUT -m owner --uid-owner 1000 -o tun0 -j ACCEPT"
	appDrop := "-A PIA_RUNTIME_OUTPUT -m owner --uid-owner 1000 -j DROP"
	pfDrop := "-A PIA_RUNTIME_OUTPUT -m owner --uid-owner 65532 -j DROP"
	established := "-A PIA_RUNTIME_OUTPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT"
	internal := "-A PIA_RUNTIME_OUTPUT -d 10.42.0.0/16 -j ACCEPT"
	for _, state := range []State{Bootstrap, Selected, Verifying, Healthy, Locked} {
		tx, err := Transaction(testConfig(), state, endpointFor(state, endpoint), false)
		if err != nil {
			t.Fatal(err)
		}
		replyIndex, dropIndex := strings.Index(tx, reply), strings.Index(tx, appDrop)
		pfDropIndex, establishedIndex := strings.Index(tx, pfDrop), strings.Index(tx, established)
		if replyIndex < 0 || !(replyIndex < dropIndex && dropIndex < pfDropIndex && pfDropIndex < establishedIndex) {
			t.Fatalf("%s identity/established ordering is unsafe", state)
		}
		if internalIndex := strings.Index(tx, internal); internalIndex < 0 || dropIndex > internalIndex {
			t.Fatalf("%s permits UID 1000 arbitrary internal traffic", state)
		}
		for _, protocol := range []string{"tcp", "udp"} {
			wan := "-A PIA_RUNTIME_OUTPUT -p " + protocol + " -d 192.0.2.10 --dport 1337 -j ACCEPT"
			if wanIndex := strings.Index(tx, wan); wanIndex >= 0 && dropIndex > wanIndex {
				t.Fatalf("%s permits UID 1000 through the direct endpoint exception", state)
			}
		}
		if state == Healthy {
			if tunnelIndex := strings.Index(tx, tunnel); !(replyIndex < tunnelIndex && tunnelIndex < dropIndex) {
				t.Fatalf("%s does not allow new and established tun0 traffic before the UID-wide drop", state)
			}
		} else if strings.Contains(tx, tunnel) {
			t.Fatalf("%s allows new or established UID 1000 tun0 traffic", state)
		}
	}
}

func TestUIDServiceRepliesAreNarrowFamilyMatchedAndOrdered(t *testing.T) {
	endpoint := Endpoint{IP: netip.MustParseAddr("192.0.2.10"), Port: 1337, PFGateway: netip.MustParseAddr("192.0.2.20")}
	for _, tc := range []struct {
		name       string
		ipv6       bool
		allowed    string
		other      string
		unapproved string
	}{
		{name: "IPv4", allowed: "10.42.0.0/16", other: "fd00:42::/64", unapproved: "203.0.113.0/24"},
		{name: "IPv6", ipv6: true, allowed: "fd00:42::/64", other: "10.42.0.0/16", unapproved: "2001:db8::/32"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, state := range []State{Bootstrap, Selected, Verifying, Healthy, Locked} {
				tx, err := Transaction(testConfig(), state, endpointFor(state, endpoint), tc.ipv6)
				if err != nil {
					t.Fatal(err)
				}
				reply := "-A PIA_RUNTIME_OUTPUT -m owner --uid-owner 1000 ! -o tun0 -p tcp --sport 8080 -d " + tc.allowed + " -m conntrack --ctstate ESTABLISHED -j ACCEPT"
				drop := "-A PIA_RUNTIME_OUTPUT -m owner --uid-owner 1000 -j DROP"
				tunnel := "-A PIA_RUNTIME_OUTPUT -m owner --uid-owner 1000 -o tun0 -j ACCEPT"
				established := "-A PIA_RUNTIME_OUTPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT"
				replyIndex, dropIndex, establishedIndex := strings.Index(tx, reply), strings.Index(tx, drop), strings.Index(tx, established)
				if replyIndex < 0 || !(replyIndex < dropIndex && dropIndex < establishedIndex) {
					t.Fatalf("%s %s service reply/drop/established ordering is unsafe", state, tc.name)
				}
				if state == Healthy && !(replyIndex < strings.Index(tx, tunnel) && strings.Index(tx, tunnel) < dropIndex) {
					t.Fatalf("%s %s service reply/tunnel/drop ordering is unsafe", state, tc.name)
				}
				if strings.Contains(tx, "--uid-owner 1000") && strings.Contains(tx, "-d "+tc.other+" -m conntrack --ctstate ESTABLISHED -j ACCEPT") {
					t.Fatalf("%s %s contains cross-family service reply rule", state, tc.name)
				}
				if strings.Contains(tx, "-d "+tc.unapproved+" -m conntrack --ctstate ESTABLISHED -j ACCEPT") {
					t.Fatalf("%s %s permits an unapproved service response destination", state, tc.name)
				}
				replyAllowances := 0
				for _, line := range strings.Split(tx, "\n") {
					if !strings.Contains(line, "--uid-owner 1000") || !strings.Contains(line, "-j ACCEPT") || strings.Contains(line, "-o tun0") && !strings.Contains(line, "! -o tun0") {
						continue
					}
					replyAllowances++
					if line != reply {
						t.Fatalf("%s %s has broad UID service allowance %q", state, tc.name, line)
					}
				}
				if replyAllowances != 1 {
					t.Fatalf("%s %s UID service reply allowances=%d", state, tc.name, replyAllowances)
				}
			}
		})
	}
}

func TestTransitionProtocolIsNarrow(t *testing.T) {
	endpoint := Endpoint{IP: netip.MustParseAddr("192.0.2.10"), Port: 1337, PFGateway: netip.MustParseAddr("192.0.2.20")}
	selected, _ := Transaction(testConfig(), Selected, endpoint, false)
	if !strings.Contains(selected, "-p tcp -d 192.0.2.10 --dport 1337") {
		t.Fatal("registration TLS exception missing")
	}
	healthy, _ := Transaction(testConfig(), Healthy, endpoint, false)
	if !strings.Contains(healthy, "-p udp -d 192.0.2.10 --dport 1337") || strings.Contains(healthy, "-p tcp -d 192.0.2.10") {
		t.Fatal("healthy endpoint exception is not UDP-only")
	}
}

func TestPFHelperAndDynamicInboundContract(t *testing.T) {
	base := Endpoint{IP: netip.MustParseAddr("192.0.2.10"), Port: 1337, PFGateway: netip.MustParseAddr("192.0.2.20")}
	pfAllow := "-A PIA_RUNTIME_OUTPUT -m owner --uid-owner 65532 -o tun0 -p tcp -d 192.0.2.20 --dport 19999 -j ACCEPT"
	pfDrop := "-A PIA_RUNTIME_OUTPUT -m owner --uid-owner 65532 -j DROP"
	established := "-A PIA_RUNTIME_OUTPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT"
	for _, state := range []State{Bootstrap, Selected, Verifying, Locked} {
		tx, err := Transaction(testConfig(), state, endpointFor(state, base), false)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(tx, pfAllow) {
			t.Fatalf("%s permits PF API access before HEALTHY", state)
		}
		if dropIndex, establishedIndex := strings.Index(tx, pfDrop), strings.Index(tx, established); dropIndex < 0 || dropIndex > establishedIndex {
			t.Fatalf("%s does not block pre-existing PF helper flows", state)
		}
	}

	healthy := base
	healthy.ForwardedPort = 49152
	ipv4, err := Transaction(testConfig(), Healthy, healthy, false)
	if err != nil {
		t.Fatal(err)
	}
	if allowIndex, dropIndex := strings.Index(ipv4, pfAllow), strings.Index(ipv4, pfDrop); allowIndex < 0 || allowIndex > dropIndex {
		t.Fatal("exact PF API allowance is missing or ordered after the PF helper drop")
	}
	for _, forbidden := range []string{
		"--uid-owner 65532 -o tun0 -j ACCEPT",
		"--uid-owner 65532 -o tun0 -p tcp -d 192.0.2.21",
		"--uid-owner 65532 -o tun0 -p tcp -d 192.0.2.20 --dport 20000",
		"--uid-owner 65532 ! -o tun0 -j ACCEPT",
	} {
		if strings.Contains(ipv4, forbidden) {
			t.Fatalf("PF helper received broad access %q", forbidden)
		}
	}
	for _, inbound := range []string{
		"-A PIA_RUNTIME_INPUT -i tun0 -p tcp --dport 49152 -m conntrack --ctstate NEW -j ACCEPT",
		"-A PIA_RUNTIME_INPUT -i tun0 -p udp --dport 49152 -m conntrack --ctstate NEW -j ACCEPT",
	} {
		if !strings.Contains(ipv4, inbound) {
			t.Fatalf("missing dynamic inbound rule %q", inbound)
		}
	}
	ipv6, err := Transaction(testConfig(), Healthy, healthy, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ipv6, "--uid-owner 65532 -o tun0 -p tcp") || strings.Contains(ipv6, "--dport 49152 -m conntrack --ctstate NEW -j ACCEPT") {
		t.Fatal("IPv4 PF gateway produced IPv6 PF allowances")
	}
	v6Endpoint := Endpoint{IP: netip.MustParseAddr("2001:db8::10"), Port: 1337, PFGateway: netip.MustParseAddr("2001:db8::20"), ForwardedPort: 49153}
	v6Healthy, err := Transaction(testConfig(), Healthy, v6Endpoint, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []string{
		"--uid-owner 65532 -o tun0 -p tcp -d 2001:db8::20 --dport 19999 -j ACCEPT",
		"-A PIA_RUNTIME_INPUT -i tun0 -p tcp --dport 49153 -m conntrack --ctstate NEW -j ACCEPT",
		"-A PIA_RUNTIME_INPUT -i tun0 -p udp --dport 49153 -m conntrack --ctstate NEW -j ACCEPT",
	} {
		if !strings.Contains(v6Healthy, rule) {
			t.Fatalf("missing IPv6 PF rule %q", rule)
		}
	}
}

type fakeRunner struct {
	mu       sync.Mutex
	hooks    map[string]bool
	restores []string
	inserts  int
}

func (r *fakeRunner) Run(_ context.Context, name string, args []string, stdin []byte) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if strings.HasSuffix(name, "restore") {
		r.restores = append(r.restores, string(stdin))
		return nil, nil
	}
	family := name
	if index := argumentIndex(args, "-I"); index >= 0 {
		key := family + ":" + args[index+1]
		r.hooks[key] = true
		r.inserts++
		return nil, nil
	}
	if index := argumentIndex(args, "-S"); index >= 0 && index+1 < len(args) {
		hook := args[index+1]
		key := family + ":" + hook
		if r.hooks[key] {
			return []byte("-P " + hook + " DROP\n-A " + hook + " -j " + chainName(hook) + "\n"), nil
		}
		return []byte("-P " + hook + " DROP\n"), nil
	}
	if len(args) > 0 && args[len(args)-1] == "-S" {
		return []byte("-P INPUT DROP\n-P OUTPUT DROP\n-P FORWARD DROP\n-N PIA_RUNTIME_INPUT\n-N PIA_RUNTIME_OUTPUT\n-N PIA_RUNTIME_FORWARD\n-A INPUT -j PIA_RUNTIME_INPUT\n-A OUTPUT -j PIA_RUNTIME_OUTPUT\n-A FORWARD -j PIA_RUNTIME_FORWARD\n"), nil
	}
	return nil, nil
}

func argumentIndex(args []string, want string) int {
	for i, arg := range args {
		if arg == want {
			return i
		}
	}
	return -1
}

func TestFirewallInitIsIdempotentAndAudited(t *testing.T) {
	runner := &fakeRunner{hooks: map[string]bool{}}
	manager := Manager{Config: testConfig(), Runner: runner}
	if err := manager.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	if runner.inserts != 6 {
		t.Fatalf("hook inserts=%d want=6", runner.inserts)
	}
	if len(runner.restores) != 4 {
		t.Fatalf("restore transactions=%d", len(runner.restores))
	}
	for _, tx := range runner.restores {
		if !strings.Contains(tx, ":OUTPUT DROP") {
			t.Fatal("restart transaction was not fail closed")
		}
	}
}

func TestInvalidStateRejected(t *testing.T) {
	if _, err := Transaction(testConfig(), State("BROKEN"), Endpoint{}, false); err == nil {
		t.Fatal("invalid state accepted")
	}
}
func TestHookMustBeFirst(t *testing.T) {
	if hookIsFirst("-P OUTPUT DROP\n-A OUTPUT -j ACCEPT\n-A OUTPUT -j PIA_RUNTIME_OUTPUT\n", "OUTPUT", "PIA_RUNTIME_OUTPUT") {
		t.Fatal("accepted a non-first hook")
	}
	if !hookIsFirst("-P OUTPUT DROP\n-A OUTPUT -j PIA_RUNTIME_OUTPUT\n-A OUTPUT -j ACCEPT\n", "OUTPUT", "PIA_RUNTIME_OUTPUT") {
		t.Fatal("rejected first hook")
	}
}
func endpointFor(state State, endpoint Endpoint) Endpoint {
	if state == Bootstrap || state == Locked {
		return Endpoint{}
	}
	return endpoint
}
