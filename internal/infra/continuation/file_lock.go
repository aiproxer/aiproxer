package continuation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	aipcont "github.com/aiproxer/aiproxer/pkg/aipsdk/continuation"
)

type fileLock struct {
	file *os.File
}

func acquireFileLock(ctx context.Context, path string) (*fileLock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: lock path is a symlink", aipcont.ErrStorageFailure)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("%w: lock stat: %w", aipcont.ErrStorageFailure, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("%w: lock open: %w", aipcont.ErrStorageFailure, err)
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("%w: lock permissions: %w", aipcont.ErrStorageFailure, err)
	}
	for {
		err = tryLockFile(f)
		if err == nil {
			return &fileLock{file: f}, nil
		}
		if !isLockBusy(err) {
			_ = f.Close()
			return nil, fmt.Errorf("%w: lock acquire: %w", aipcont.ErrStorageFailure, err)
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = f.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (l *fileLock) close() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := unlockFile(l.file)
	closeErr := l.file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

func lockPath(path string) string { return filepath.Clean(path + ".lock") }
