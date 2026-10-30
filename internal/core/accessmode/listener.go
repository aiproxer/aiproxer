package accessmode

import (
	"net"
	"strings"
)

// Surface classifies a listen address for access policy.
type Surface string

const (
	SurfaceMalformed   Surface = "malformed"
	SurfaceLoopback    Surface = "loopback"
	SurfaceBroad       Surface = "broad"
	SurfaceNonLoopback Surface = "non_loopback"
)

// ListenClassification is the parsed listen address plus a conservative surface kind.
// Hostname binds (other than localhost) are always SurfaceNonLoopback because loopback
// cannot be proven without resolution.
type ListenClassification struct {
	Raw     string
	Host    string
	Port    string
	Surface Surface
}

// ClassifyListenAddress parses host/port and classifies bind posture.
func ClassifyListenAddress(raw string) (ListenClassification, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ListenClassification{Raw: raw, Surface: SurfaceMalformed}, ErrMalformedListenAddress
	}

	host, port, err := net.SplitHostPort(s)
	if err != nil {
		// Bare IP or hostname without a port (legacy configs): classify surface with empty Port.
		// Other validation may require an explicit port (host:port) and map port-less values to
		// [SurfaceMalformed] with [ErrMalformedListenAddress] instead of this path.
		h := strings.TrimSpace(s)
		if ip := net.ParseIP(h); ip != nil {
			switch {
			case ip.IsUnspecified():
				return ListenClassification{Raw: raw, Host: h, Port: port, Surface: SurfaceBroad}, nil
			case ip.IsLoopback():
				return ListenClassification{Raw: raw, Host: h, Port: port, Surface: SurfaceLoopback}, nil
			default:
				return ListenClassification{Raw: raw, Host: h, Port: port, Surface: SurfaceNonLoopback}, nil
			}
		}
		if strings.EqualFold(h, "localhost") {
			return ListenClassification{Raw: raw, Host: h, Port: port, Surface: SurfaceLoopback}, nil
		}
		// Legacy bare host without port is supported above for IPs/localhost; other shapes are malformed.
		return ListenClassification{Raw: raw, Surface: SurfaceMalformed}, ErrMalformedListenAddress
	}

	host = strings.TrimSpace(host)
	port = strings.TrimSpace(port)
	if port == "" {
		return ListenClassification{Raw: raw, Surface: SurfaceMalformed}, ErrMalformedListenAddress
	}

	// All IPv4 interfaces or IPv6 unspecified.
	if host == "" {
		return ListenClassification{Raw: raw, Host: host, Port: port, Surface: SurfaceBroad}, nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsUnspecified() {
			return ListenClassification{Raw: raw, Host: host, Port: port, Surface: SurfaceBroad}, nil
		}
		if ip.IsLoopback() {
			return ListenClassification{Raw: raw, Host: host, Port: port, Surface: SurfaceLoopback}, nil
		}
		return ListenClassification{Raw: raw, Host: host, Port: port, Surface: SurfaceNonLoopback}, nil
	}
	if strings.EqualFold(host, "localhost") {
		return ListenClassification{Raw: raw, Host: host, Port: port, Surface: SurfaceLoopback}, nil
	}
	return ListenClassification{Raw: raw, Host: host, Port: port, Surface: SurfaceNonLoopback}, nil
}
