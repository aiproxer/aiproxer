// Package completion defines the completion-gate extension contract (design §6, R8).
// Gates observe a bounded buffered canonical event slice for one attempt and return
// a typed outcome; core enforces buffer limits and fail-open behavior on errors.
package completion
