//go:build !windows

package runtimebundle_test

import "os"

func replaceTestFile(from, to string) error {
	return os.Rename(from, to)
}
