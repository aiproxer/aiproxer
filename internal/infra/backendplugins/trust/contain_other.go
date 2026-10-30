//go:build !windows

package trust

import "os"

// confirmOpenedUnderRoot is a no-op outside Windows; POSIX openNoFollow already
// uses O_NOFOLLOW and lexical resolveUnderRoot / underRootAbs handle escapes.
func confirmOpenedUnderRoot(string, *os.File) error { return nil }
