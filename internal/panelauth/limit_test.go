package panelauth

import (
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSourceAdmissionIsolationExpiryAndMemoryBound(t *testing.T) {
	var limiter sourceAdmission
	now := time.Unix(1000, 0)
	for i := 0; i < publicRequestsPerMinute; i++ {
		if !limiter.allow("attacker", now) {
			t.Fatal("early throttle")
		}
	}
	if limiter.allow("attacker", now) {
		t.Fatal("source limit ignored")
	}
	if !limiter.allow("owner", now) {
		t.Fatal("one source blocked another")
	}
	if !limiter.allow("attacker", now.Add(time.Minute)) {
		t.Fatal("limit did not expire")
	}
	for i := 0; i < maxAdmissionSources+1; i++ {
		limiter.allow(fmt.Sprint(i), now)
	}
	if len(limiter.buckets) > maxAdmissionSources || limiter.lru.Len() > maxAdmissionSources {
		t.Fatal("unbounded source memory")
	}
	if !limiter.allow("new-owner", now) {
		t.Fatal("full map blocked a new owner")
	}
}

func TestProxyHeadersTrustedOnlyOnRenderAndIPv6RotationIsBounded(t *testing.T) {
	r := httptest.NewRequest("POST", "/auth/login", nil)
	r.RemoteAddr = "192.0.2.5:1234"
	r.Header.Set("X-Forwarded-For", "198.51.100.1, 10.0.0.1")
	h := &HTTP{}
	if h.source(r) != "192.0.2.5" {
		t.Fatal("untrusted XFF bypassed source limits")
	}
	h.TrustRenderProxy = true
	if h.source(r) != "198.51.100.1" {
		t.Fatal("Render client source ignored")
	}
	r.Header.Add("X-Forwarded-For", "203.0.113.10")
	if h.source(r) != "192.0.2.5" {
		t.Fatal("ambiguous proxy headers trusted")
	}
	r.Header.Del("X-Forwarded-For")
	r.RemoteAddr = "[2001:db8:abcd:1234::1]:99"
	first := h.source(r)
	r.RemoteAddr = "[2001:db8:abcd:1234::999]:1234"
	if first != h.source(r) {
		t.Fatal("IPv6 addresses in the same /64 got separate budgets")
	}
}
