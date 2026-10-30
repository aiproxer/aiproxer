package transporterr

import (
	"context"
	"errors"
	"net"
	"net/url"
	"os"
	"syscall"
	"testing"
)

type timeoutNetError struct{}

func (timeoutNetError) Error() string   { return "i/o timeout" }
func (timeoutNetError) Timeout() bool   { return true }
func (timeoutNetError) Temporary() bool { return true }

type nonTimeoutNetError struct{}

func (nonTimeoutNetError) Error() string   { return "other net error" }
func (nonTimeoutNetError) Timeout() bool   { return false }
func (nonTimeoutNetError) Temporary() bool { return false }

func TestIsRetryable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain", errors.New("boom"), false},
		{"context_canceled", context.Canceled, false},
		{"context_deadline", context.DeadlineExceeded, false},
		{"net_timeout", &url.Error{Op: "Post", URL: "http://x", Err: timeoutNetError{}}, true},
		{"bare_net_timeout", timeoutNetError{}, true},
		{"net_non_timeout", &url.Error{Op: "Post", URL: "http://x", Err: nonTimeoutNetError{}}, false},
		{
			"conn_reset",
			&url.Error{Op: "Post", URL: "http://x", Err: &os.SyscallError{Syscall: "read", Err: syscall.ECONNRESET}},
			true,
		},
		{
			"conn_refused",
			&url.Error{Op: "Post", URL: "http://x", Err: &os.SyscallError{Syscall: "connect", Err: syscall.ECONNREFUSED}},
			true,
		},
		{
			"conn_aborted",
			&url.Error{Op: "Post", URL: "http://x", Err: &os.SyscallError{Syscall: "write", Err: syscall.ECONNABORTED}},
			true,
		},
		{"dns_not_found", &net.DNSError{Err: "no such host", Name: "x", IsNotFound: true}, true},
		{"dns_timeout", &net.DNSError{Err: "timeout", Name: "x", IsTimeout: true}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := IsRetryable(tt.err); got != tt.want {
				t.Fatalf("IsRetryable(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
