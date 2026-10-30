package main

import (
	"regexp"
	"strings"
)

// nativeProcessHostGateTests are the process-host tests the native
// cross-platform gate must always select.
//
// They are the platform guarantees a native runner is the only evidence for:
// the diagnostic budget that terminates a flooding connector, the output
// capture the budget charges, and the process-tree termination that keeps a
// descendant from holding the host's drain open. A native Windows run that
// skipped them would report green without proving any of it.
//
// The names are literal test names, not prefixes, so a rename cannot leave a
// stale entry that silently matches nothing.
var nativeProcessHostGateTests = []string{
	// The diagnostic budget against a real flooding connector.
	"TestHost_NativeFloodingConnectorIsTerminated",
	"TestFloodFixture_SurvivesClosedOutputPipe",
	// The launcher hands the host the child's real standard streams.
	"TestPlatformLauncher_CapturesChildOutput",
	// Both drained streams are charged against the one budget.
	"TestHost_NativeBothStreamsShareTheBudget",
	// A descendant that holds the connector's output open does not survive
	// the termination.
	"TestHost_NativeProcessTreeIsTerminated",
	// The Windows production path: named pipe peer gating, job ownership,
	// substitution refusal and the held launch handle.
	"TestWindows_PipeBytePing",
	"TestWindows_PipeGRPCInProcess",
	"TestWindows_SecureChannel_LaunchJobPeerConfigure",
	"TestWindows_SubstitutionDeniedWhileHeld",
	"TestWindows_HeldHandleSurvivesArtifactClose",
	"TestWindows_PeerRejectWrongPID",
	"TestWindows_JobKillOnClose",
	"TestWindows_NoConfigBeforeAuth",
	"TestWindowsChildCapture_ParentHandlesAreNotInheritable",
	// The inherited-handle list outlives the native process creation, and
	// concurrent termination never uses a disposed handle.
	"TestWindowsChildCapture_AttrHandlesOutliveConstruction",
	"TestWindowsProc_ConcurrentSignalKillClose",
}

// nativeProcessHostGatePattern returns the anchored -run pattern that selects
// exactly the mandatory process-host tests.
func nativeProcessHostGatePattern() string {
	return "^(" + strings.Join(nativeProcessHostGateTests, "|") + ")$"
}

// nativeProcessHostGateRegex compiles the mandatory selection pattern.
func nativeProcessHostGateRegex() *regexp.Regexp {
	return regexp.MustCompile(nativeProcessHostGatePattern())
}
