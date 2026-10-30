//go:build !unix

package main

// ignoreBrokenPipeSignal is a no-op where a broken pipe is reported as a
// write error instead of a signal (Windows). The fixture already keeps
// running after a failed write, so a closed output pipe does not end it.
func ignoreBrokenPipeSignal() {}
