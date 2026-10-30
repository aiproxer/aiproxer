package auth

import (
	"net"
	"strings"
)

// peerIPFromRemoteAddr extracts the host from r.RemoteAddr. Forwarded headers must not be used.
// Host-only values (no port) are returned trimmed as-is.
func peerIPFromRemoteAddr(remoteAddr string) string {
	remoteAddr = strings.TrimSpace(remoteAddr)
	if remoteAddr == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}
