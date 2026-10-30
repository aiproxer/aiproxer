// Package injection applies a bounded, provider-neutral continuity projection
// to a canonical call. It owns only the pure request mutation; durable branch
// pending/release state remains with the preserver and coordinator layers.
package injection
