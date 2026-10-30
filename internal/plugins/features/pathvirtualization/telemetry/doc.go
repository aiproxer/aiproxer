// Package telemetry is the content-free observability projection of the B-leg path
// virtualization feature: requirements.md 7.6's counters, 7.7's content freedom, 7.8's
// diagnostics inventory, and 9.5's realized-savings accounting, in one recorder the
// composition seam installs on the three components it builds.
//
// It exists as its own subpackage for the same reason outbound, expansion, and config
// do. It reads config.Resolved.Shape(), outbound.Report, and expansion.Report, and the
// lexical core at internal/plugins/features/pathvirtualization keeps no repository
// import at all - so a projection that imports those packages cannot live inside the
// core without weakening the core's own host-authority guard.
//
// WHAT IT OWNS, and the four decisions that shape it:
//
//  1. IT IS A RECORDER, NOT A REGISTRY. There is one *Telemetry per built bundle, handed
//     to each component as a construction option. There is no package-level state, no
//     init() registration, no service locator, and no ambient hook: a deployment that
//     wants the counters constructs the bundle and reads the recorder, and a deployment
//     that does not simply never reads one. A recorder installed by a global would make
//     the feature's observability reachable from anywhere, which is the construction
//     rule this repository forbids outright.
//
//  2. EVERY DIMENSION IS A CLOSED ENUM, AND THE SERIES ARE HELD IN VOCABULARY ORDER.
//     The four dimensions are the rollout mode, the direction, the outcome, and the
//     reason - and each is one of the feature's own already-closed enums, projected
//     through their String() methods. Nothing assembles a label from input: a value
//     outside a closed vocabulary degrades to that enum's own "unknown", which is what
//     keeps a hostile payload from inventing a series. Series are emitted in vocabulary
//     order rather than in first-seen order, so identical traffic produces byte-
//     identical output and a metrics export does not reorder itself between reads.
//
//  3. THE TWO DIRECTIONS ARE SEPARATE, AND ONLY ONE OF THEM HAS A SAVING. This is the
//     decision that needs stating most loudly, because getting it wrong produces a
//     number that flatters nobody and misleads everybody. Both directions replace a
//     DECODED string value with another decoded string value, and the shared engine
//     measures decoded-value lengths in both cases. Outbound, the replacement is the
//     alias, which requirement 9.1 guarantees is strictly shorter than the real root, so
//     before-minus-after is a genuine saving. Inbound, the replacement is the real root,
//     which is necessarily LONGER than the alias it replaced, so the same subtraction is
//     NEGATIVE: expanding an alias back to a real path COSTS bytes rather than saving
//     them. Summing both directions into one "bytes saved" figure would report a
//     deployment that virtualizes heavily as losing on every expansion - arithmetically
//     true, operationally useless. So the projection keeps the directions apart, reports
//     the inbound delta as BytesGrown rather than as a saving, and never publishes a
//     negative saving even if a delta is negative.
//
//  4. IT RECORDS, IT NEVER DECIDES. Nothing here can change what a component publishes.
//     A pass built without a reporter behaves identically to a pass built with one; the
//     recorder is a sink on a side channel, and every field it publishes is read from a
//     report the component had already decided to emit. That is what makes the
//     observability safe to add to a feature whose requirements put it in a
//     safety-relevant position.
//
// The recorder is safe for concurrent use and says so in its own contract: one instance
// is shared by every retry, race participant, and failover candidate of a logical turn,
// because the bundle hands the same instance to all three of its components and the
// runtime holds those components for the whole generation.
package telemetry
