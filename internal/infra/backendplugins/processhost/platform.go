package processhost

type PlatformID string

const (
	PlatformLinux   PlatformID = "linux"
	PlatformDarwin  PlatformID = "darwin"
	PlatformWindows PlatformID = "windows"
)

type PlatformVerificationStatus string

const (
	PlatformRuntimeVerified       PlatformVerificationStatus = "runtime_verified"
	PlatformDesignSourceEvidenced PlatformVerificationStatus = "design_source_evidenced"
	PlatformCompileUnverified     PlatformVerificationStatus = "compile_unverified"
)

type PlatformProfile struct {
	Platform      PlatformID
	Verification  PlatformVerificationStatus
	LaunchBinding string
	LocalChannel  string
}

// HostSecureProfile is the release/QA inventory of launch + local-channel readiness.
// Darwin remains compile-only until an approved peer-cred channel replaces fail-closed.
type HostSecureProfile struct {
	Platform             PlatformID
	Verification         PlatformVerificationStatus
	LaunchBinding        string
	LocalChannel         string
	RuntimeChannelOK     bool
	RuntimeChannelReason string
}

func ApprovedPlatformProfiles() []PlatformProfile {
	return []PlatformProfile{
		{
			Platform:      PlatformLinux,
			Verification:  PlatformDesignSourceEvidenced,
			LaunchBinding: "sealed_or_immutable_descriptor_execveat_empty_path",
			LocalChannel:  "private_af_unix_so_peercred_expected_generation",
		},
		{
			Platform:      PlatformDarwin,
			Verification:  PlatformCompileUnverified,
			LaunchBinding: "protected_private_digest_staging_path_launch",
			LocalChannel:  "fail_closed_unsupported_channel_until_peercred_profile",
		},
		{
			Platform:      PlatformWindows,
			Verification:  PlatformDesignSourceEvidenced,
			LaunchBinding: "protected_private_digest_staging_path_launch",
			LocalChannel:  "named_pipe_dacl_token_pid_job_expected_generation",
		},
	}
}

func HostSecureProfiles() []HostSecureProfile {
	out := make([]HostSecureProfile, 0, 3)
	for _, p := range ApprovedPlatformProfiles() {
		h := HostSecureProfile{
			Platform:      p.Platform,
			Verification:  p.Verification,
			LaunchBinding: p.LaunchBinding,
			LocalChannel:  p.LocalChannel,
		}
		if RuntimeChannelSupported(p.Platform) {
			h.RuntimeChannelOK = true
		} else {
			h.RuntimeChannelReason = "host_channel_fail_closed:" + string(ReasonUnsupportedChannel)
		}
		out = append(out, h)
	}
	return out
}

func RuntimeChannelSupported(p PlatformID) bool {
	switch p {
	case PlatformLinux, PlatformWindows:
		return true
	default:
		return false
	}
}
