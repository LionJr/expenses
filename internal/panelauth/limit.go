package panelauth

import (
	"container/list"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

const publicRequestsPerMinute = 30
const maxAdmissionSources = 4096

type admissionBucket struct {
	key   string
	reset time.Time
	count int
}

// Admission is bounded and process-local; persistent MFA budgets live in
// PostgreSQL. Eviction never denies all new clients when the map is full.
type sourceAdmission struct {
	mu      sync.Mutex
	buckets map[string]*list.Element
	lru     list.List
}

func (a *sourceAdmission) allow(key string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.buckets == nil {
		a.buckets = make(map[string]*list.Element)
	}
	e := a.buckets[key]
	if e == nil {
		if len(a.buckets) >= maxAdmissionSources {
			last := a.lru.Back()
			delete(a.buckets, last.Value.(*admissionBucket).key)
			a.lru.Remove(last)
		}
		e = a.lru.PushFront(&admissionBucket{key: key, reset: now.Add(time.Minute)})
		a.buckets[key] = e
	}
	a.lru.MoveToFront(e)
	b := e.Value.(*admissionBucket)
	if !now.Before(b.reset) {
		b.reset = now.Add(time.Minute)
		b.count = 0
	}
	if b.count >= publicRequestsPerMinute {
		return false
	}
	b.count++
	return true
}

func (h *HTTP) source(r *http.Request) string {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	// Render overwrites the first XFF address with the actual client address.
	// Opt in only on Render, where public traffic comes via its edge. Generic
	// and local deployments never trust client-supplied proxy headers.
	if h.TrustRenderProxy {
		values := r.Header.Values("X-Forwarded-For")
		if len(values) == 1 {
			first, _, _ := strings.Cut(values[0], ",")
			if ip, err := netip.ParseAddr(strings.TrimSpace(first)); err == nil {
				return sourceKey(ip)
			}
		}
	}
	ip, err := netip.ParseAddr(peer)
	if err != nil {
		return "unknown-peer"
	}
	return sourceKey(ip)
}

func sourceKey(ip netip.Addr) string {
	ip = ip.Unmap().WithZone("")
	if ip.Is6() {
		return netip.PrefixFrom(ip, 64).Masked().String()
	}
	return ip.String()
}
