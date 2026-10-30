//go:build darwin

package trust

import (
	"os"

	"golang.org/x/sys/unix"
)

func openNoFollow(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	if f == nil {
		_ = unix.Close(fd)
		return nil, ReasonOpenFailed
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = f.Close()
		return nil, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = f.Close()
		return nil, ReasonNotRegular
	}
	return f, nil
}
