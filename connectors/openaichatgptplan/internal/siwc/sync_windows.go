//go:build windows

package siwc

// Windows does not expose a portable directory fsync through the Go API.
// The publication of a record uses the file system directly, so the
// available durability comes from the file sync before the publication.
func syncDirectory(string) error { return nil }
