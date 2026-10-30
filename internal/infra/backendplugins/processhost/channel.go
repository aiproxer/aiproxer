package processhost

import (
	"context"
	"net"
	"os"
)

// PeerIdentity is the authenticated local peer for an accepted connection.
type PeerIdentity struct {
	PID        int
	UID        int
	SID        string // Windows token user SID when available
	Generation uint64
}

// LaunchEnvProvider supplies non-secret channel bootstrap env entries.
type LaunchEnvProvider interface {
	LaunchEnv() []string
}

// Listener is an approved confidential local channel endpoint.
type Listener interface {
	Accept(ctx context.Context) (net.Conn, PeerIdentity, error)
	Close() error
}

// ChannelFactory creates an approved local channel for one generation.
// Inherited may contain confidential one-shot handles for the child (never env).
type ChannelFactory interface {
	Listen(ctx context.Context, generation uint64) (lis Listener, inherited []*os.File, err error)
}

// PlatformChannel is the OS-approved production channel factory.
type PlatformChannel struct{}

func NewPlatformChannel() *PlatformChannel { return &PlatformChannel{} }
