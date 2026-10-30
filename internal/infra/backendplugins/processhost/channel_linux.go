//go:build linux

package processhost

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// channelChildFD is the deterministic ExtraFiles index (3 + i → child sees 3+i).
// os/exec places ExtraFiles[0] at FD 3.
const channelChildFD = 3

// Listen creates a connected AF_UNIX socketpair. The child end is returned in
// inherited for LaunchSpec.ExtraFiles; the host end is authenticated via
// SO_PEERCRED after the child process exists. No filesystem UDS path is used.
func (PlatformChannel) Listen(ctx context.Context, generation uint64) (Listener, []*os.File, error) {
	_ = ctx
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	hostFile := os.NewFile(uintptr(fds[0]), "aip-bp-host")
	childFile := os.NewFile(uintptr(fds[1]), "aip-bp-child")
	if hostFile == nil || childFile == nil {
		_ = unix.Close(fds[0])
		_ = unix.Close(fds[1])
		return nil, nil, ReasonUnsupportedChannel
	}
	hc, err := net.FileConn(hostFile)
	_ = hostFile.Close()
	if err != nil {
		_ = childFile.Close()
		return nil, nil, err
	}
	uc, ok := hc.(*net.UnixConn)
	if !ok {
		_ = hc.Close()
		_ = childFile.Close()
		return nil, nil, ReasonUnsupportedChannel
	}
	lis := &unixPairListener{
		conn:        uc,
		generation:  generation,
		expectedPID: -1,
		expectedUID: -1,
	}
	return lis, []*os.File{childFile}, nil
}

type unixPairListener struct {
	mu          sync.Mutex
	conn        *net.UnixConn
	generation  uint64
	expectedPID int
	expectedUID int
	accepted    bool
	closed      bool
}

func (u *unixPairListener) SetExpectedPID(pid int) { u.expectedPID = pid }
func (u *unixPairListener) SetExpectedUID(uid int) { u.expectedUID = uid }

func (u *unixPairListener) Accept(ctx context.Context) (net.Conn, PeerIdentity, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed || u.conn == nil {
		return nil, PeerIdentity{}, ReasonPeerRejected
	}
	if u.accepted {
		return nil, PeerIdentity{}, ReasonPeerRejected
	}
	select {
	case <-ctx.Done():
		_ = u.conn.Close()
		u.conn = nil
		u.closed = true
		return nil, PeerIdentity{}, ctx.Err()
	default:
	}
	peer, err := peercred(u.conn)
	if err != nil {
		_ = u.conn.Close()
		u.conn = nil
		u.closed = true
		return nil, PeerIdentity{}, err
	}
	peer.Generation = u.generation
	// SO_PEERCRED on socketpair(2) reports the credentials of the process that
	// created the pair (the host), not the child that inherited the peer FD.
	// Trust comes from FD inheritance via ExtraFiles; override PID with the
	// known child PID so authorizePeer/ContainsPID sees the correct process.
	// UID from SO_PEERCRED remains valid and is still checked below.
	if u.expectedPID > 0 {
		peer.PID = u.expectedPID
	}
	if u.expectedUID >= 0 && peer.UID != u.expectedUID {
		_ = u.conn.Close()
		u.conn = nil
		u.closed = true
		return nil, PeerIdentity{}, ReasonPeerRejected
	}
	c := u.conn
	u.conn = nil
	u.accepted = true
	return c, peer, nil
}

func (u *unixPairListener) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.closed = true
	if u.conn == nil {
		return nil
	}
	err := u.conn.Close()
	u.conn = nil
	return err
}

func peercred(c *net.UnixConn) (PeerIdentity, error) {
	rc, err := c.SyscallConn()
	if err != nil {
		return PeerIdentity{}, err
	}
	var cred *unix.Ucred
	var ctrlErr error
	err = rc.Control(func(fd uintptr) {
		cred, ctrlErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	})
	if err != nil {
		return PeerIdentity{}, err
	}
	if ctrlErr != nil {
		return PeerIdentity{}, ctrlErr
	}
	if cred == nil || cred.Pid <= 0 {
		return PeerIdentity{}, fmt.Errorf("%w", ReasonPeerRejected)
	}
	return PeerIdentity{PID: int(cred.Pid), UID: int(cred.Uid)}, nil
}
