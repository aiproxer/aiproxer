package localstream

// Package localstream provides the generic canonical proxy-local response
// stream used by successful local turns. It is producer-neutral: it carries
// exactly one assistant text payload with a finite response/message/text/
// terminal sequence, no usage event, no B-leg identity, and no background
// goroutine. Runtime and frontend contract tests consume the same helper so
// that reply-tag identity equals encoded/decoded assistant content across
// streaming and non-streaming official frontends.
