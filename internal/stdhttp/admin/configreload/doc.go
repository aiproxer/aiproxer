// Package configreload implements the process-owned management HTTP adapter for
// explicit configuration reload and status (spec versioned-runtime-reloadable-proxy-configuration
// task 5.3; requirements 1.3, 1.7, 12.1-12.11, 13.1-13.2).
//
// The listener is startup-fixed and loopback by default. Handlers never accept
// paths, YAML, URLs, commands, or plugin-install instructions. Authentication
// and browser-origin guards run before the reload coordinator is invoked.
// Accepted reload attempts continue under a host-owned context after client
// disconnect; terminal results remain available from status.
package configreload
