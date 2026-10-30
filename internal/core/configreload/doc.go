// Package configreload owns typed field-level reloadability policy for runtime
// configuration changes. The secret-safe reload trigger/result/status vocabulary
// is declared once in pkg/aipsdk/configreload; this package owns history,
// sanitization, stage constants, and policy algorithms only. Trigger envelopes
// never carry paths, YAML, or URLs.
package configreload
