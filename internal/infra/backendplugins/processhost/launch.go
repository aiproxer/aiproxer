package processhost

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/trust"
)

// LaunchSpec describes a direct, no-shell activation request.
// It never carries connector secrets or bootstrap key material.
type LaunchSpec struct {
	Artifact   *trust.VerifiedArtifact
	WorkDir    string
	Env        []string // minimal non-secret allowlist only; non-nil
	Generation uint64
	// ExtraFiles are confidential one-shot inherited handles (never env bootstrap).
	ExtraFiles []*os.File
}

// Process is a supervised child identity for peer checks and cleanup.
type Process interface {
	PID() int
	Generation() uint64
	Wait() (err error)
	SignalKill() error
	GracefulStop(timeout time.Duration) error
	Close() error
	ContainsPID(pid int) bool
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
}

// Launcher starts an exact verified artifact identity.
type Launcher interface {
	Launch(ctx context.Context, spec LaunchSpec) (Process, error)
}

// PlatformLauncher is the OS-approved production launcher.
type PlatformLauncher struct{}

func NewPlatformLauncher() *PlatformLauncher { return &PlatformLauncher{} }
