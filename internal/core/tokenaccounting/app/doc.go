// Package app coordinates token counting through provider and local tokenizer ports.
//
// The package owns orchestration policy only: provider-vs-local mode selection,
// fallback behavior, context checks, and result metadata defaults. Concrete
// provider count APIs, tokenizer adapters, persistence, runtime integration, and
// protocol endpoints live outside this package.
package app
