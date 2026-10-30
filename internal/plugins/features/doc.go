// Package features contains the standard-distribution feature plugins.
//
// Constructor convention:
//
// Feature plugin entrypoints return the hook interface they register rather than
// exposing concrete hook structs. This is an intentional plugin-boundary
// exception to the usual "accept interfaces, return structs" guideline: callers
// only need the stable hook contract, while concrete implementations remain
// package-local. Constructors should therefore use role-specific names such as
// NewSubmitHook, NewRequestPartHook, NewResponsePartHook, or NewToolReactor.
package features
