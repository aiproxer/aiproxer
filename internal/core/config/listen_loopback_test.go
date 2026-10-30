package config_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/config"
)

func TestIsExplicitLoopbackListenAddress(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		addr string
		want bool
	}{
		{name: "bare_port", addr: ":8080", want: false},
		{name: "ipv4_wildcard_with_port", addr: "0.0.0.0:8080", want: false},
		{name: "ipv6_wildcard_with_port", addr: "[::]:8080", want: false},
		{name: "ipv4_loopback_with_port", addr: "127.0.0.1:8080", want: true},
		{name: "ipv4_loopback_host_only", addr: "127.0.0.1", want: true},
		{name: "localhost_with_port", addr: "localhost:8080", want: true},
		{name: "ipv6_loopback_with_port", addr: "[::1]:8080", want: true},
		{name: "ipv6_loopback_host_only", addr: "::1", want: true},
		{name: "localhost_host_only", addr: "localhost", want: true},
		{name: "empty", addr: "", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := config.IsExplicitLoopbackListenAddress(tc.addr); got != tc.want {
				t.Fatalf("addr=%q: want %v got %v", tc.addr, tc.want, got)
			}
		})
	}
}
