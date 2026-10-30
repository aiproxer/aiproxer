package manifest

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/aiproxer/aiproxer/pkg/aipsdk"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
)

const (
	MaxManifestBytes   = 256 << 10
	MaxExports         = 64
	MaxPlatforms       = 32
	MaxExtensions      = 8
	MaxStringBytes     = 512
	MaxPluginIDBytes   = 128
	MaxExecutableBytes = 256
)

var (
	sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)
	pluginID  = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,126}[a-z0-9])?$`)
	kindID    = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,62}[a-z0-9])?$`)
)

// Validate checks semantic bounds of a decoded Manifest without I/O.
func (m Manifest) Validate() error {
	if m.Schema != SchemaV1 {
		return fmt.Errorf("%w: %q", ErrUnknownSchema, m.Schema)
	}
	if err := boundString("plugin_id", m.PluginID, MaxPluginIDBytes); err != nil {
		return err
	}
	if !pluginID.MatchString(m.PluginID) {
		return fmt.Errorf("%w: plugin_id", ErrInvalidManifest)
	}
	if err := boundString("version", m.Version, MaxStringBytes); err != nil {
		return err
	}
	if strings.TrimSpace(m.Version) == "" {
		return fmt.Errorf("%w: version", ErrInvalidManifest)
	}
	if err := boundString("build_id", m.BuildID, MaxStringBytes); err != nil {
		return err
	}
	if err := boundString("executable", m.Executable, MaxExecutableBytes); err != nil {
		return err
	}
	if err := validateExecutablePath(m.Executable); err != nil {
		return err
	}
	if !sha256Hex.MatchString(m.SHA256) {
		return fmt.Errorf("%w", ErrInvalidDigest)
	}
	if m.ProtocolMajor != 1 {
		return fmt.Errorf("%w: protocol_major", ErrInvalidManifest)
	}
	if m.ProtocolMaxMinor < m.ProtocolMinMinor {
		return fmt.Errorf("%w: protocol minor range", ErrInvalidManifest)
	}
	if len(m.Platforms) == 0 || len(m.Platforms) > MaxPlatforms {
		return fmt.Errorf("%w: platforms", ErrBoundsExceeded)
	}
	for _, p := range m.Platforms {
		if err := validatePlatform(p); err != nil {
			return err
		}
	}
	if len(m.Exports) == 0 || len(m.Exports) > MaxExports {
		return fmt.Errorf("%w: exports", ErrBoundsExceeded)
	}
	seen := map[string]struct{}{}
	for _, e := range m.Exports {
		if err := boundString("kind", e.Kind, MaxStringBytes); err != nil {
			return err
		}
		if !kindID.MatchString(e.Kind) {
			return fmt.Errorf("%w: kind", ErrInvalidManifest)
		}
		if _, ok := seen[e.Kind]; ok {
			return fmt.Errorf("%w: %s", ErrDuplicateExport, e.Kind)
		}
		seen[e.Kind] = struct{}{}
		if err := backendplugin.ValidateCredentialMode(e.CredentialMode); err != nil {
			return fmt.Errorf("%w: credential_mode", ErrInvalidManifest)
		}
		if err := backendplugin.ValidateAccessScope(e.AccessScope); err != nil {
			return fmt.Errorf("%w: access_scope", ErrInvalidManifest)
		}
		if err := e.ProcessSharing.Validate(); err != nil {
			return fmt.Errorf("%w: process_sharing", ErrInvalidManifest)
		}
		if err := (aipsdk.BackendExecutionProfile{Class: e.ExecutionClass}).Validate(); err != nil {
			return fmt.Errorf("%w: execution_class", ErrInvalidManifest)
		}
		if err := validateExportSecurityPosture(e); err != nil {
			return err
		}
		if err := validateExportCredentialContract(e); err != nil {
			return err
		}
		if err := boundString("display_name", e.DisplayName, MaxStringBytes); err != nil {
			return err
		}
		if err := boundString("description", e.Description, MaxStringBytes); err != nil {
			return err
		}
	}
	if len(m.Extensions) > MaxExtensions {
		return fmt.Errorf("%w: extensions", ErrBoundsExceeded)
	}
	if len(m.Extensions) == 0 {
		return nil
	}
	// v1 allowlist is empty: any named extension is unsupported.
	ext := m.Extensions[0]
	if err := boundString("extension.name", ext.Name, MaxStringBytes); err != nil {
		return err
	}
	if ext.Name == "" || ext.Version == 0 {
		return fmt.Errorf("%w", ErrUnsupportedExtension)
	}
	return fmt.Errorf("%w: %s", ErrUnsupportedExtension, ext.Name)
}

// validateExportSecurityPosture rejects an export whose declared credential mode
// contradicts its declared access scope. It never infers a value: both fields stay
// mandatory declarations and this check only proves they agree.
//
// credential_mode: oauth_user is a user-scoped credential. The host principal and
// credential model never multiplexes such a credential across unrelated principals,
// so an oauth_user export must declare access_scope: local_only. Advertising
// access_scope: any there would be internally contradictory: the host already
// refuses the backend in multi-user mode, while the manifest would claim broad
// eligibility.
func validateExportSecurityPosture(e Export) error {
	if e.CredentialMode != backendplugin.CredentialModeOAuthUser {
		return nil
	}
	if e.AccessScope == backendplugin.AccessScopeLocalOnly {
		return nil
	}
	return fmt.Errorf(
		"%w: %w: export %q declares credential_mode %q which requires access_scope %q, got %q",
		ErrInvalidManifest,
		ErrInconsistentExportSecurityPosture,
		e.Kind,
		backendplugin.CredentialModeOAuthUser,
		backendplugin.AccessScopeLocalOnly,
		e.AccessScope,
	)
}

// validateExportCredentialContract enforces the closed static-key contract
// rule: static provider-secret exports must declare a valid versioned
// credential contract, and no other credential mode may carry one. Both
// directions fail closed with the offending export kind named; no secret
// value can appear because contracts hold namespace identity only.
func validateExportCredentialContract(e Export) error {
	if e.CredentialMode == backendplugin.CredentialModeStatic {
		if e.CredentialContract == nil {
			return fmt.Errorf(
				"%w: %w: export %q declares credential_mode %q without a credential contract",
				ErrInvalidManifest,
				ErrMissingCredentialContract,
				e.Kind,
				backendplugin.CredentialModeStatic,
			)
		}
		if err := e.CredentialContract.Validate(); err != nil {
			return fmt.Errorf("%w: export %q: %v", ErrInvalidManifest, e.Kind, err)
		}
		return nil
	}
	if e.CredentialContract != nil {
		return fmt.Errorf(
			"%w: %w: export %q declares credential_mode %q with a credential contract",
			ErrInvalidManifest,
			ErrUnexpectedCredentialContract,
			e.Kind,
			e.CredentialMode,
		)
	}
	return nil
}

func boundString(field, v string, maxBytes int) error {
	if !utf8.ValidString(v) {
		return fmt.Errorf("%w: %s utf8", ErrInvalidManifest, field)
	}
	if len(v) > maxBytes {
		return fmt.Errorf("%w: %s", ErrBoundsExceeded, field)
	}
	return nil
}

func validateExecutablePath(p string) error {
	if p == "" || path.IsAbs(p) || strings.Contains(p, `\`) {
		return fmt.Errorf("%w", ErrInvalidExecutable)
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return fmt.Errorf("%w", ErrInvalidExecutable)
	}
	if clean != p {
		return fmt.Errorf("%w: not normalized", ErrInvalidExecutable)
	}
	base := path.Base(p)
	lower := strings.ToLower(base)
	for _, bad := range []string{".sh", ".bash", ".zsh", ".ps1", ".bat", ".cmd", ".py", ".js", ".rb", ".pl"} {
		if strings.HasSuffix(lower, bad) {
			return fmt.Errorf("%w: script/interpreter", ErrInvalidExecutable)
		}
	}
	if slices.Contains([]string{"cmd.exe", "powershell.exe", "pwsh.exe", "bash", "sh", "python", "python3", "node"}, lower) {
		return fmt.Errorf("%w: interpreter entrypoint", ErrInvalidExecutable)
	}
	return nil
}

func validatePlatform(p Platform) error {
	switch p.OS {
	case "windows", "linux", "darwin":
	default:
		return fmt.Errorf("%w: os %q", ErrInvalidPlatform, p.OS)
	}
	switch p.Arch {
	case "amd64", "arm64":
	default:
		return fmt.Errorf("%w: arch %q", ErrInvalidPlatform, p.Arch)
	}
	return nil
}

// RequiresWindowsEXE reports whether any platform requires a .exe executable suffix.
func (m Manifest) RequiresWindowsEXE() bool {
	for _, p := range m.Platforms {
		if p.OS == "windows" {
			return true
		}
	}
	return false
}
