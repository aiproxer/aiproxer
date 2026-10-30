//go:build !windows && !linux && !darwin

package discovery

import "os"

func openRegular(string) (*os.File, error) {
	return nil, stringError("unsupported platform")
}
