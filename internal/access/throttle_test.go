package access

import (
	"fmt"
	"net/netip"
	"testing"
)

func v4(i int) netip.Addr {
	return netip.MustParseAddr(fmt.Sprintf("%d.%d.%d.%d", 10+i>>24&0xff, i>>16&0xff, i>>8&0xff, i&0xff))
}

func TestThrottleMapBounded(t *testing.T) {
	th := NewThrottle()
	for i := 0; i < 25000; i++ {
		th.Attempt(v4(i))
	}
	if n := len(th.m); n > maxTracked {
		t.Fatalf("tracking %d IPs, cap %d", n, maxTracked)
	}
}

func TestThrottleKeepsBlockedIPsWhenFull(t *testing.T) {
	th := NewThrottle()
	victim := netip.MustParseAddr("203.0.113.9")
	for i := 0; i < maxFailures+1; i++ {
		th.Attempt(victim)
	}
	if th.Attempt(victim) {
		t.Fatal("victim should be blocked")
	}
	for i := 0; i < 25000; i++ {
		th.Attempt(v4(i))
	}
	if th.Attempt(victim) {
		t.Fatal("flooding other IPs evicted a blocked IP")
	}
}

func TestThrottleGroupsIPv6By64(t *testing.T) {
	th := NewThrottle()
	allowed := 0
	for i := 0; i < 30; i++ {
		ip := netip.MustParseAddr(fmt.Sprintf("2001:db8:1:2::%x", i+1)) // one /64, varying interface id
		if th.Attempt(ip) {
			allowed++
		}
	}
	if allowed > maxFailures {
		t.Fatalf("%d attempts allowed from one /64 (limit %d)", allowed, maxFailures)
	}
}
