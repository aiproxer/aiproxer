// Package terminalwork implements the pure-domain durable terminal-work item
// state machine and store command shapes (requirements 8.1–8.9, design D9).
//
// Work rows are independently idempotent actions with stable source keys,
// claim leases, and injectable-clock retry schedules. Domain code has no SQL,
// HTTP, or filesystem I/O. The application processor lives in the app subpackage;
// durable stores live under internal/infra/terminalwork/workstore.
package terminalwork
