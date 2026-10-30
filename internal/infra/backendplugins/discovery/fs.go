package discovery

import (
	"io"
	"io/fs"
	"os"
)

// FS is the minimal filesystem surface discovery needs.
type FS interface {
	Lstat(name string) (fs.FileInfo, error)
	ReadDir(name string) ([]fs.DirEntry, error)
	Open(name string) (*os.File, error)
}

type osFS struct{}

func (osFS) Lstat(name string) (fs.FileInfo, error)     { return os.Lstat(name) }
func (osFS) ReadDir(name string) ([]fs.DirEntry, error) { return os.ReadDir(name) }
func (osFS) Open(name string) (*os.File, error)         { return openRegular(name) }

// SpawnProbe is an optional per-Discoverer hook. Production discovery never calls it.
type SpawnProbe interface {
	NoteLaunchAttempt()
}

// Config configures deterministic non-executing discovery.
type Config struct {
	ExplicitPaths           []string
	PackagerRoots           []string
	IncludeUpstreamDefaults bool
	Development             bool
	FS                      FS
	SpawnProbe              SpawnProbe
}

func openBounded(f *os.File, maxBytes int64) ([]byte, error) {
	limited := io.LimitReader(f, maxBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > maxBytes {
		return nil, errTooLarge
	}
	return raw, nil
}

var errTooLarge = stringError("manifest too large")

// ErrSymlinkRejected reports a symlink encountered where a regular file is
// required. Compare with errors.Is so wrapped causes still classify as a
// symlink rejection instead of a generic open failure.
var ErrSymlinkRejected = stringError("symlink_rejected")

type stringError string

func (e stringError) Error() string { return string(e) }
