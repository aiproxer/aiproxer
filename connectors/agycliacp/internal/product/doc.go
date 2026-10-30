// Package agycliacp implements the Antigravity CLI (agy) backend via the
// go-agy-acp-wrapper binary using the Agent Control Protocol over stdio.
// The wrapper proxies JSON-RPC between the proxy and the agy CLI, handling
// authentication (methodId: "agy") and session lifecycle. AGY models are
// fully qualified with vendor namespaces (e.g. "google/gemini-3.5-flash-high").
package product
