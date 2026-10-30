//go:build windows

package oauthcred

// Windows does not expose a portable directory fsync through the Go API.
// os.Rename replaces the file via MoveFileEx with MOVEFILE_REPLACE_EXISTING,
// so the available durability comes from the file sync before the rename.
func syncDirectory(string) error { return nil }
