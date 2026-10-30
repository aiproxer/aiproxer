// Package configsource provides the bounded fixed-path configuration source for
// runtime reload (versioned-runtime-reloadable-proxy-configuration).
//
// [FixedSource] resolves an absolute startup path and implements ReadStable with
// handle re-stat, path revalidation, and atomic-replacement enforcement. This
// package must not grow a file watcher, mtime poller, or automatic reload loop.
package configsource
