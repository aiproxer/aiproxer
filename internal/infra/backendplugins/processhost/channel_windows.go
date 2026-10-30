//go:build windows

package processhost

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"unsafe"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"golang.org/x/sys/windows"
)

// errJobDuplicate reports that the connector's job handle could not be
// duplicated for the listener. The membership check cannot run without it.
var errJobDuplicate = errors.New("processhost: duplicate job handle")

const (
	pipeBufSize    = 64 << 10
	pipeNamePrefix = `\\.\pipe\aip-bp-`
)

// Listen creates a private local named pipe for one generation.
// The server handle uses FILE_FLAG_OVERLAPPED so gRPC concurrent I/O cannot deadlock.
func (PlatformChannel) Listen(ctx context.Context, generation uint64) (Listener, []*os.File, error) {
	_ = ctx
	var rnd [8]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return nil, nil, err
	}
	name := pipeNamePrefix + itoa(int(generation)) + "-" + hex.EncodeToString(rnd[:])
	sa, err := pipeSecurityAttributes()
	if err != nil {
		return nil, nil, err
	}
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, nil, err
	}
	h, err := windows.CreateNamedPipe(
		p,
		windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED|windows.FILE_FLAG_FIRST_PIPE_INSTANCE,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
		1,
		pipeBufSize,
		pipeBufSize,
		0,
		sa,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: CreateNamedPipe: %v", ReasonUnsupportedChannel, err)
	}
	return &namedPipeListener{
		name:        name,
		handle:      h,
		generation:  generation,
		expectedPID: -1,
	}, nil, nil
}

type namedPipeListener struct {
	mu          sync.Mutex
	name        string
	handle      windows.Handle
	generation  uint64
	expectedPID int
	expectedSID string
	// job is the listener's own duplicate of the connector's job object.
	// It is independently owned: the connector's Close may dispose the
	// original while this listener still needs the membership check.
	job windows.Handle
	// jobBroken is set when the job handle could not be duplicated. The
	// membership check cannot run without it, so Accept fails closed.
	jobBroken bool
	accepted  bool
	closed    bool
}

func (l *namedPipeListener) LaunchEnv() []string {
	return []string{backendplugin.EnvChannelPipe + "=" + l.name}
}

func (l *namedPipeListener) SetExpectedPID(pid int)    { l.expectedPID = pid }
func (l *namedPipeListener) SetExpectedSID(sid string) { l.expectedSID = sid }
func (l *namedPipeListener) SetJobFromProcess(p Process) {
	// The listener stores an independently owned duplicate, not the
	// connector's handle: the connector's Close may dispose the original
	// while this listener still needs the membership check. Duplication
	// under the process mutex closes the read-then-duplicate window.
	dj, ok := p.(interface {
		DuplicateJobHandle() (windows.Handle, error)
	})
	if !ok {
		return
	}
	dup, err := dj.DuplicateJobHandle()
	if err != nil || dup == 0 {
		l.jobBroken = true
		return
	}
	l.job = dup
}

func (l *namedPipeListener) Accept(ctx context.Context) (net.Conn, PeerIdentity, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.handle == 0 {
		return nil, PeerIdentity{}, ReasonPeerRejected
	}
	if l.accepted {
		return nil, PeerIdentity{}, ReasonPeerRejected
	}
	if err := connectNamedPipeCancelable(ctx, l.handle); err != nil {
		_ = windows.CloseHandle(l.handle)
		l.handle = 0
		l.closed = true
		return nil, PeerIdentity{}, err
	}

	var clientPID uint32
	if err := windows.GetNamedPipeClientProcessId(l.handle, &clientPID); err != nil {
		_ = windows.CloseHandle(l.handle)
		l.handle = 0
		l.closed = true
		return nil, PeerIdentity{}, fmt.Errorf("%w: client pid: %v", ReasonPeerRejected, err)
	}
	if l.expectedPID > 0 && int(clientPID) != l.expectedPID {
		_ = windows.CloseHandle(l.handle)
		l.handle = 0
		l.closed = true
		return nil, PeerIdentity{}, ReasonPeerRejected
	}
	sid, err := processUserSID(clientPID)
	if err != nil {
		_ = windows.CloseHandle(l.handle)
		l.handle = 0
		l.closed = true
		return nil, PeerIdentity{}, fmt.Errorf("%w: token: %v", ReasonPeerRejected, err)
	}
	if l.expectedSID != "" && sid != l.expectedSID {
		_ = windows.CloseHandle(l.handle)
		l.handle = 0
		l.closed = true
		return nil, PeerIdentity{}, ReasonPeerRejected
	}
	if l.jobBroken {
		_ = windows.CloseHandle(l.handle)
		l.handle = 0
		l.closed = true
		return nil, PeerIdentity{}, fmt.Errorf("%w: %v", ReasonPeerRejected, errJobDuplicate)
	}
	if l.job != 0 {
		ph, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, clientPID)
		if err != nil {
			_ = windows.CloseHandle(l.handle)
			l.handle = 0
			l.closed = true
			return nil, PeerIdentity{}, ReasonPeerRejected
		}
		var inJob bool
		err = isProcessInJob(ph, l.job, &inJob)
		_ = windows.CloseHandle(ph)
		if err != nil || !inJob {
			_ = windows.CloseHandle(l.handle)
			l.handle = 0
			l.closed = true
			return nil, PeerIdentity{}, ReasonPeerRejected
		}
	}

	conn := newPipeConn(l.handle, l.name)
	l.handle = 0
	l.accepted = true
	return conn, PeerIdentity{
		PID:        int(clientPID),
		SID:        sid,
		Generation: l.generation,
	}, nil
}

func (l *namedPipeListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	// The job duplicate is owned by the listener, not borrowed from the
	// connector, so it is released here.
	if l.job != 0 {
		_ = windows.CloseHandle(l.job)
		l.job = 0
	}
	if l.handle == 0 {
		return nil
	}
	err := windows.CloseHandle(l.handle)
	l.handle = 0
	return err
}

func connectNamedPipeCancelable(ctx context.Context, h windows.Handle) error {
	evt, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(evt) }()
	ov := &windows.Overlapped{HEvent: evt}
	err = windows.ConnectNamedPipe(h, ov)
	if err == nil || err == windows.ERROR_PIPE_CONNECTED {
		return nil
	}
	if err != windows.ERROR_IO_PENDING {
		return err
	}
	for {
		s, waitErr := windows.WaitForSingleObject(evt, 50)
		if waitErr != nil {
			_ = windows.CancelIoEx(h, ov)
			return waitErr
		}
		if s == windows.WAIT_OBJECT_0 {
			var n uint32
			if err := windows.GetOverlappedResult(h, ov, &n, false); err != nil && err != windows.ERROR_PIPE_CONNECTED {
				return err
			}
			return nil
		}
		select {
		case <-ctx.Done():
			_ = windows.CancelIoEx(h, ov)
			return ctx.Err()
		default:
		}
	}
}

func pipeSecurityAttributes() (*windows.SecurityAttributes, error) {
	sid, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return nil, err
	}
	adminsSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return nil, err
	}
	dacl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		},
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(systemSID),
			},
		},
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.NO_INHERITANCE,
			Trustee: windows.TRUSTEE{
				TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_WELL_KNOWN_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(adminsSID),
			},
		},
	}, nil)
	if err != nil {
		return nil, err
	}
	sd, err := windows.NewSecurityDescriptor()
	if err != nil {
		return nil, err
	}
	if err := sd.SetDACL(dacl, true, false); err != nil {
		return nil, err
	}
	return &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
		InheritHandle:      0,
	}, nil
}

func currentUserSID() (*windows.SID, error) {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return nil, err
	}
	defer func() { _ = token.Close() }()
	tu, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	return tu.User.Sid, nil
}

func processUserSID(pid uint32) (string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return "", err
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var token windows.Token
	if err := windows.OpenProcessToken(h, windows.TOKEN_QUERY, &token); err != nil {
		return "", err
	}
	defer func() { _ = token.Close() }()
	tu, err := token.GetTokenUser()
	if err != nil {
		return "", err
	}
	return tu.User.Sid.String(), nil
}
