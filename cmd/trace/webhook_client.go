package main

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"
)

var errWebhookDestination = errors.New("webhook destination address is not allowed")

// Address ranges that are never public webhook destinations. Link-local
// (which includes cloud metadata services) is refused even when the operator
// allows private networks.
var (
	webhookAlwaysBlocked = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("169.254.0.0/16"),
		netip.MustParsePrefix("224.0.0.0/4"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("fe80::/10"),
		netip.MustParsePrefix("ff00::/8"),
		netip.MustParsePrefix("::/128"),
	}
	webhookPrivate = []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("fc00::/7"),
		netip.MustParsePrefix("64:ff9b::/96"),
		netip.MustParsePrefix("64:ff9b:1::/48"),
	}
)

// webhookAddressAllowed decides whether Trace may connect to addr for a
// webhook. Loopback is allowed only for hooks configured with a loopback
// host (local development); private ranges only when the operator enabled
// them with `trace serve -webhook-allow-private-networks`.
func webhookAddressAllowed(addr netip.Addr, allowLoopback, allowPrivate bool) bool {
	addr = addr.Unmap()
	if !addr.IsValid() {
		return false
	}
	for _, prefix := range webhookAlwaysBlocked {
		if prefix.Contains(addr) {
			return false
		}
	}
	if addr.Is4() && addr == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
		return false
	}
	if addr.IsLoopback() {
		return allowLoopback
	}
	for _, prefix := range webhookPrivate {
		if prefix.Contains(addr) {
			return allowPrivate
		}
	}
	return !addr.IsUnspecified() && !addr.IsMulticast() && !addr.IsLinkLocalUnicast()
}

// webhookHTTPClient returns a delivery client that checks every address it
// connects to (after DNS resolution, so rebinding cannot bypass it), never
// uses environment proxies, and does not follow redirects.
func webhookHTTPClient(allowLoopback, allowPrivate bool) *http.Client {
	dialer := &net.Dialer{
		Timeout: 5 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return errWebhookDestination
			}
			addr, err := netip.ParseAddr(host)
			if err != nil || !webhookAddressAllowed(addr, allowLoopback, allowPrivate) {
				return errWebhookDestination
			}
			return nil
		},
	}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
	}
	return &http.Client{
		Timeout:       5 * time.Second,
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// clientForWebhook returns the delivery client for one configured hook URL.
func (s *store) clientForWebhook(rawURL string) *http.Client {
	allowLoopback := false
	if u, err := url.Parse(rawURL); err == nil {
		allowLoopback = isLoopback(u.Hostname())
	}
	return webhookHTTPClient(allowLoopback, s.webhookAllowPrivate)
}
