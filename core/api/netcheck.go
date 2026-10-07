package api

import (
	"net"
	"net/http"
	"net/netip"

	"github.com/davitizhgenti/hostd/sdk"
)

// privateOnly refuses requests whose peer address is not on a home
// network: loopback, RFC 1918, link-local, or IPv6 unique local. IPv4
// clients on a dual-stack socket arrive as ::ffff:a.b.c.d and are unwrapped
// first, so a public IPv4 address cannot slip through as "IPv6".
func privateOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedPeer(r.RemoteAddr) {
			writeError(w, r, http.StatusForbidden, sdk.CodeForbidden, "requests are only accepted from the local network")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func allowedPeer(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	addr = addr.Unmap().WithZone("")
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast()
}
