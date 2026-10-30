// Package terminaldecision defines the provider-neutral SDK contract for
// bounded provisional-terminal decisions.
//
// The canonical input contains value DTOs only. A provider may inspect that
// input and return a bounded decision; the platform remains responsible for
// terminal claims, stream mutation, backend work, and every other effect.
package terminaldecision
