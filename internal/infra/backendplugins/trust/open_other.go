//go:build !windows && !linux && !darwin

package trust

import "os"

func openNoFollow(string) (*os.File, error) {
	return nil, ReasonStagingUnsupported
}
