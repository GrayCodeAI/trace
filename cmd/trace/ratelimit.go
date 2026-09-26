package main

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Trace throttles each client per minute: 300 HTTP requests across all paths,
// 20 sign-in POSTs, and 60 new SSH connections. Counters live in memory and
// are shared by every limiter for the same data directory within one process;
// they reset on restart and are not coordinated between processes or nodes
// (use the reverse proxy's limiter for that).

const (
	rateWindowLength = time.Minute
	httpRateLimit    = 300
	loginRateLimit   = 20
	sshRateLimit     = 60
)

type rateWindow struct {
	Started time.Time
	Count   int
}

type rateState struct {
	mu        sync.Mutex
	windows   map[string]rateWindow
	lastPrune time.Time
}

var (
	rateStatesMu sync.Mutex
	rateStates   = map[string]*rateState{}
)

type rateLimiter struct {
	state *rateState
	// trustedProxies are peers whose X-Forwarded-For / X-Real-IP headers
	// identify the real client. Headers from any other peer are ignored.
	trustedProxies []netip.Prefix
}

type rateDecision struct {
	Allowed   bool
	Limit     int
	Remaining int
	Reset     time.Time
}

func newRateLimiter(root string) *rateLimiter {
	rateStatesMu.Lock()
	defer rateStatesMu.Unlock()
	state, ok := rateStates[root]
	if !ok {
		state = &rateState{windows: map[string]rateWindow{}}
		rateStates[root] = state
	}
	return &rateLimiter{state: state}
}

// parseTrustedProxies parses a comma-separated list of IP addresses and CIDR
// prefixes for `trace serve -trusted-proxy`.
func parseTrustedProxies(value string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(item); err == nil {
			out = append(out, prefix.Masked())
			continue
		}
		addr, err := netip.ParseAddr(item)
		if err != nil {
			return nil, errors.New("-trusted-proxy entries must be IP addresses or CIDR prefixes")
		}
		addr = addr.Unmap()
		out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return out, nil
}

func (l *rateLimiter) trusted(addr netip.Addr) bool {
	for _, prefix := range l.trustedProxies {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// clientAddr returns the address a request came from: the peer, or, when the
// peer is a trusted proxy, the rightmost X-Forwarded-For entry that is not
// itself a trusted proxy (entries further left are client-controlled), then
// X-Real-IP.
func (l *rateLimiter) clientAddr(r *http.Request) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(strings.TrimSpace(host))
	if err != nil {
		return netip.Addr{}, false
	}
	peer = peer.Unmap()
	if !l.trusted(peer) {
		return peer, true
	}
	var hops []string
	for _, header := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(header, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		addr, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		if addr = addr.Unmap(); !l.trusted(addr) {
			return addr, true
		}
	}
	if addr, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Real-IP"))); err == nil {
		return addr.Unmap(), true
	}
	return peer, true
}

// clientKey identifies a client for throttling. IPv6 clients are grouped by
// their /64 so rotating addresses within one allocation does not evade it.
func (l *rateLimiter) clientKey(r *http.Request) string {
	addr, ok := l.clientAddr(r)
	if !ok {
		if strings.TrimSpace(r.RemoteAddr) != "" {
			return strings.TrimSpace(r.RemoteAddr)
		}
		return "local-unknown"
	}
	return addrKey(addr)
}

func addrKey(addr netip.Addr) string {
	if addr.Is6() && !addr.Is4In6() {
		prefix, err := addr.Prefix(64)
		if err == nil {
			return prefix.String()
		}
	}
	return addr.String()
}

func requestRateLimit(r *http.Request) (string, int) {
	if r.URL.Path == "/login" && r.Method == http.MethodPost {
		return "login", loginRateLimit
	}
	return "http", httpRateLimit
}

func (l *rateLimiter) allow(r *http.Request) rateDecision {
	class, limit := requestRateLimit(r)
	return l.allowKey(class+"\x00"+l.clientKey(r), limit)
}

func (l *rateLimiter) allowSSH(remoteAddr string) rateDecision {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil || host == "" {
		host = strings.TrimSpace(remoteAddr)
	}
	key := host
	if addr, err := netip.ParseAddr(host); err == nil {
		key = addrKey(addr.Unmap())
	}
	if key == "" {
		key = "local-unknown"
	}
	return l.allowKey("ssh\x00"+key, sshRateLimit)
}

func (l *rateLimiter) allowKey(key string, limit int) rateDecision {
	now := time.Now().UTC()
	st := l.state
	st.mu.Lock()
	defer st.mu.Unlock()
	if now.Sub(st.lastPrune) >= rateWindowLength {
		for oldKey, old := range st.windows {
			if now.Sub(old.Started) >= rateWindowLength || old.Started.After(now) {
				delete(st.windows, oldKey)
			}
		}
		st.lastPrune = now
	}
	item, ok := st.windows[key]
	if !ok || item.Started.After(now) || now.Sub(item.Started) >= rateWindowLength {
		item = rateWindow{Started: now}
	}
	item.Count++
	st.windows[key] = item
	remaining := limit - item.Count
	if remaining < 0 {
		remaining = 0
	}
	return rateDecision{Allowed: item.Count <= limit, Limit: limit, Remaining: remaining, Reset: item.Started.Add(rateWindowLength)}
}

func writeRateHeaders(w http.ResponseWriter, decision rateDecision) {
	w.Header().Set("X-RateLimit-Limit", strconv.Itoa(decision.Limit))
	w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(decision.Remaining))
	w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(decision.Reset.Unix(), 10))
}
