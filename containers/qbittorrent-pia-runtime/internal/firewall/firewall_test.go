package firewall

import (
	"context"
	"net/netip"
	"strings"
	"sync"
	"testing"
)

func testConfig() Config {
	return Config{AllowedSubnets: []netip.Prefix{netip.MustParsePrefix("10.42.0.0/16"), netip.MustParsePrefix("fd00:42::/64")}, ApplicationUID: 1000, ServicePort: 8080, Interface: "tun0"}
}

func TestCompleteTransactionsAndNoUIDDefaultAllowance(t *testing.T) {
	endpoint := Endpoint{IP: netip.MustParseAddr("192.0.2.10"), Port: 1337}
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
			if !strings.Contains(tx, "--uid-owner 1000 ! -o tun0 -j DROP") {
				t.Fatalf("%s lacks explicit UID default-interface block", state)
			}
			if strings.Index(tx, "--uid-owner 1000 ! -o tun0 -j DROP") > strings.Index(tx, "-A PIA_RUNTIME_OUTPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT") {
				t.Fatalf("%s evaluates established output before UID block", state)
			}
			if state != Healthy && strings.Contains(tx, "--uid-owner 1000 -o tun0 -j ACCEPT") {
				t.Fatalf("%s permits application tunnel traffic", state)
			}
		}
	}
}

func TestUIDServiceRepliesAreNarrowFamilyMatchedAndOrdered(t *testing.T) {
	endpoint := Endpoint{IP: netip.MustParseAddr("192.0.2.10"), Port: 1337}
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
				drop := "-A PIA_RUNTIME_OUTPUT -m owner --uid-owner 1000 ! -o tun0 -j DROP"
				established := "-A PIA_RUNTIME_OUTPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT"
				replyIndex, dropIndex, establishedIndex := strings.Index(tx, reply), strings.Index(tx, drop), strings.Index(tx, established)
				if replyIndex < 0 || !(replyIndex < dropIndex && dropIndex < establishedIndex) {
					t.Fatalf("%s %s service reply/drop/established ordering is unsafe", state, tc.name)
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
	endpoint := Endpoint{IP: netip.MustParseAddr("192.0.2.10"), Port: 1337}
	selected, _ := Transaction(testConfig(), Selected, endpoint, false)
	if !strings.Contains(selected, "-p tcp -d 192.0.2.10 --dport 1337") {
		t.Fatal("registration TLS exception missing")
	}
	healthy, _ := Transaction(testConfig(), Healthy, endpoint, false)
	if !strings.Contains(healthy, "-p udp -d 192.0.2.10 --dport 1337") || strings.Contains(healthy, "-p tcp -d 192.0.2.10") {
		t.Fatal("healthy endpoint exception is not UDP-only")
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
