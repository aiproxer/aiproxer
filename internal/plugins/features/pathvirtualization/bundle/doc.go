// Package bundle is the composition seam of the B-leg path virtualization feature.
//
// It exists as its own package for one reason. design.md "Configuration and
// Composition" requires the feature's three components to reach the runtime through
// the existing extension planes, and reaching a plane means importing
// pkg/aipsdk/feature. The lexical core at
// internal/plugins/features/pathvirtualization keeps NO repository import at all and
// proves it with a host-authority guard, so anything that reads the SDK's plane and
// bundle contracts has to live beside that core rather than inside it - the same
// reason outbound and expansion are subpackages.
//
// The package is deliberately thin. It owns two exported functions and no state:
//
//   - it turns ONE compiled configuration into the attempt transform, the
//     request-part hook, and the path-expansion finalizer, each contributed through
//     an already-registered plane;
//   - it passes the resolved rollout mode to every component as an explicit
//     argument, because rewrite.Mode's zero value is audit and a component built
//     without one would silently measure where the operator asked to mutate;
//   - it hands all three components the SAME compiled policy resolver, so the early
//     and late outbound passes cannot disagree about which surface is path-bearing;
//   - it constructs nothing at all for a disabled resolution, so requirements.md
//     7.1's "disabled by default" is a property of the compiled value rather than
//     of a caller's discipline;
//   - it installs exactly ONE content-free recorder - the feature's own
//     telemetry.Telemetry, built from the compiled configuration's SHAPE and handed
//     over as a bare identifier - on each of the three components, which is what
//     discharges requirements.md 7.6's counters and 7.8's inventory. No closure is
//     composed here and no label is attached here, because a label this package
//     composed would be the one observable dimension whose boundedness were a matter
//     of discipline rather than of construction;
//   - and it introduces no new generic plane, because Task 7.3 established that the
//     registered finalizer plane carries the whole mandatory-buffering declaration.
//
// The second exported function exists only so the recorder is reachable. A registry
// factory returns a bundle and nothing else, so the shipped composition installs the
// recorder and no deployment reads it - which is the correct outcome for a stock install,
// and means a deployment that wants the counters asks for the recorder rather than
// reaching for one. There is no global, no registry, and no configuration key that hands
// the recorder to arbitrary code.
//
// There is deliberately no workspace authority to decide about. The runtime resolves
// one workspace view per logical turn, pins it, and projects it onto BOTH stages the
// two outbound passes read: the early pass out of request.AttemptMeta.Workspace, the
// late pass out of the public SDK context seam
// (pkg/aipsdk/workspace.WithWorkspaceView, published by internal/core/execctx.WithViews
// alongside session, scope, and principal). This builder therefore needs no workspace
// input at all, and a host needs no workspace view of its own to arm the late pass -
// which is what a FeatureFactory parameter carrying one would have required, and a
// factory signature like that is a service locator. Reading one pin is also what makes
// requirements.md 5.6 true by construction rather than by a composition root happening
// to hold the right resolver chain. A generation whose runtime pinned nothing gets the
// documented fail-open direction instead: publish nothing, keep the real path, record a
// bounded reason.
//
// This package imports no internal/core package and no sibling feature. The
// cross-feature rule that involves a sibling feature's operator key is owned by the
// config subpackage and is CALLED from the composition root, which is the only place
// that holds the whole registration list.
package bundle
