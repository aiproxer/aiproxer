// Package controltool defines the provider-neutral SDK contract for one
// optional proxy-owned model control tool.
//
// A provider exposes a frozen Spec (name, JSON Schema, instruction, and args
// budget) and consumes a completed call plus request-local provenance. This
// package validates those contracts; it is not a tool runtime, service locator,
// or DI container. Tool names are provider-owned data at this layer.
package controltool
