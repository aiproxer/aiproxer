//go:build !windows

package stdhttp

import "os"

func detectRunningAsAdmin() (bool, error) {
	return os.Geteuid() == 0, nil
}
