package service

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/aiproxer/aiproxer/connectors/openaichatgptplan/internal/responses"
	"gopkg.in/yaml.v3"
)

// Config is the typed per-instance configuration of the direct factory.
//
// The connector owns the SIWC boundary: the protected local store that holds
// the saved registrations and the explicitly selected registration inside it.
// No credential material may appear in this configuration, because the
// effective credential is the selected user's SIWC OAuth profile.
type Config struct {
	// ProfileStorePath is the filesystem path of the protected local store
	// that holds the saved SIWC registrations.
	//
	// The store is a directory: it holds one protected document per saved
	// registration and the explicitly selected registration next to them,
	// so that one registration's record can be replaced without touching
	// another's.
	//
	// The path must be absolute. A relative path would be resolved against
	// the working directory of whichever process started the connector, so
	// the same configuration would address a different directory after a
	// restart with a different working directory - a silent change of which
	// registrations the connector can see.
	//
	// The path may name the directory through symlinks and ".." components.
	// The store resolves the path the way the operating system does and
	// manages the records in the resolved directory, so the directory that
	// holds the records is the resolved one - the one the operating system
	// will really use.
	//
	// Empty means this instance has no registration store. Its account-scoped
	// model inventory then fails explicitly instead of reading a store the
	// operator did not configure; the connector never guesses a store on the
	// operator's behalf.
	//
	// The directory that holds the records must not be writable by other
	// users: a record's own permissions protect its contents, but a
	// principal that can write the directory can replace the record. A
	// group-writable directory is accepted as an operator's deliberate
	// trust-domain choice, and the guarantee then holds only inside that
	// group. Only the immediate parent is checked, so the ancestors of the
	// store path are a trusted precondition.
	ProfileStorePath string `yaml:"profile_store_path"`
	// ProfileID selects one saved registration explicitly. Empty means the
	// store's active registration; selection never rotates to another
	// registration on its own.
	ProfileID string `yaml:"profile_id"`
	// HostRecordPath is the filesystem path of the protected local store
	// that holds this installation's stable agent-host identity.
	//
	// The host record is a separate file from the profile store on purpose:
	// an imported profile carries the host id of the machine it was created
	// on, and importing it must not overwrite the destination host's own
	// host id. Empty means the per-installation default location.
	//
	// The path must be absolute. A relative path would be resolved against
	// the working directory of whichever process started the connector, so
	// the same configuration would address a different file after a restart
	// with a different working directory, which would silently change the
	// host identity.
	//
	// The path must name a file. A path whose last component is empty, "."
	// or ".." does not name the record, and it is refused at parse time
	// instead of by the store that owns the record.
	//
	// The path may name the record through symlinks and ".." components.
	// The store resolves the path the way the operating system does and
	// manages the record at the resolved location, so the directory that
	// holds the record is the resolved parent - the one the operating
	// system will really use.
	//
	// The directory that holds the record must not be writable by other
	// users: the record's own permissions protect its contents, but a
	// principal that can write the directory can replace the record. A
	// group-writable directory is accepted as an operator's deliberate
	// trust-domain choice, and the guarantee then holds only inside that
	// group. Only the immediate parent is checked, so the ancestors of the
	// record path are a trusted precondition: a principal that can write an
	// ancestor can replace the directory itself.
	//
	// The durability guarantee has two preconditions: every directory of
	// the record's chain must be readable, and the filesystem must support
	// flushing a directory. An ancestor that grants traversal but not
	// reading makes the first start fail with an explicit permission error
	// instead of an unproven host id.
	HostRecordPath string `yaml:"host_record_path"`
	// PolicyMode is the operator's SIWC preview admission posture.
	//
	// Empty means the documented default, responses.PolicyModeInteroperable: a
	// request whose unsupported controls can be omitted is served, and every
	// omission is recorded. responses.PolicyModeStrict refuses such a request
	// instead. Both postures refuse the same unusable requests - a hosted tool,
	// a continuation without history, and a wire document that violates the
	// preview contract - and neither changes how supported content is rendered.
	//
	// A posture this build does not know is refused at Configure: silently
	// falling back to the default would serve a strict operator the
	// degradation they asked to refuse.
	PolicyMode responses.PolicyMode `yaml:"policy_mode"`
}

// ErrResponsesBaseURLNotConfigurable reports a configuration that tries to name
// the Responses endpoint.
//
// The endpoint is NOT an operator setting. Requirement 4.2 sends the selected
// registration's bearer credential only to the documented public resource, so
// the shipped configuration surface has no endpoint key at all: not for a
// private route, not for a look-alike host, and not for a local reference
// server. The reference server exists only in the connector's test-only
// construction path, which cannot be reached from YAML or Configure.
//
// The failure is typed and explicit instead of a generic unknown-key error, so
// an operator who copied an older example learns WHICH key is gone and why.
var ErrResponsesBaseURLNotConfigurable = errors.New(
	"openai-chatgpt-plan: config: responses_base_url is not configurable; the connector always sends to the documented public Responses endpoint",
)

// forbiddenSecretKeys are configuration keys that would carry literal
// credential material. The effective credential is the stored SIWC profile,
// so a literal token under one of these names is always a mistake and is
// rejected with a specific error instead of being read.
//
// The configuration subtree is decoded strictly (unknown keys are rejected),
// so a literal credential under any other name is rejected as an unknown
// configuration key rather than being silently ignored.
var forbiddenSecretKeys = []string{
	"access_token",
	"refresh_token",
	"id_token",
	"api_key",
	"apikey",
	"token",
	"secret",
	"client_secret",
	"pat",
	"authorization_code",
	"code_verifier",
}

// ParseConfigYAML parses the opaque configuration subtree the host hands to
// Configure.
//
// The subtree is closed: only the keys declared by Config are accepted, and
// the named credential keys are rejected with an explicit error. A key this
// connector does not know is a configuration error, never a silently ignored
// value.
func ParseConfigYAML(raw []byte) (Config, error) {
	var cfg Config
	if len(bytes.TrimSpace(raw)) > 0 {
		var probe map[string]any
		if err := yaml.Unmarshal(raw, &probe); err != nil {
			return Config{}, fmt.Errorf("openai-chatgpt-plan: config: %w", err)
		}
		for key := range probe {
			normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
			if normalized == "responses_base_url" {
				// The endpoint is not an operator setting: refusing the key by
				// name is clearer than the decoder's generic unknown-field
				// error, and it states the rule that replaced the old knob.
				return Config{}, fmt.Errorf("%w (key %q)", ErrResponsesBaseURLNotConfigurable, key)
			}
			if slices.Contains(forbiddenSecretKeys, normalized) {
				return Config{}, fmt.Errorf(
					"openai-chatgpt-plan: config: literal credential key %q is forbidden; the effective credential is the selected user's SIWC profile store",
					key,
				)
			}
		}
		dec := yaml.NewDecoder(bytes.NewReader(raw))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
			return Config{}, fmt.Errorf("openai-chatgpt-plan: config: %w", err)
		}
	}
	cfg.ProfileStorePath = strings.TrimSpace(cfg.ProfileStorePath)
	cfg.ProfileID = strings.TrimSpace(cfg.ProfileID)
	cfg.HostRecordPath = strings.TrimSpace(cfg.HostRecordPath)
	posture, err := responses.ParsePolicyMode(strings.TrimSpace(string(cfg.PolicyMode)))
	if err != nil {
		return Config{}, fmt.Errorf("openai-chatgpt-plan: config: %w", err)
	}
	cfg.PolicyMode = posture
	if cfg.ProfileStorePath != "" {
		if !filepath.IsAbs(cfg.ProfileStorePath) {
			return Config{}, fmt.Errorf(
				"openai-chatgpt-plan: config: profile_store_path must be an absolute path; a relative path would address a different directory after a restart with a different working directory",
			)
		}
	}
	if cfg.HostRecordPath != "" {
		if !filepath.IsAbs(cfg.HostRecordPath) {
			return Config{}, fmt.Errorf(
				"openai-chatgpt-plan: config: host_record_path must be an absolute path; a relative path would address a different file after a restart with a different working directory",
			)
		}
		if !hostRecordPathNamesFile(cfg.HostRecordPath) {
			return Config{}, fmt.Errorf(
				"openai-chatgpt-plan: config: host_record_path must name a file; a path whose last component is empty, \".\" or \"..\" does not name the host record",
			)
		}
	}
	return cfg, nil
}

// hostRecordPathNamesFile reports whether the configured host record path
// names a file.
//
// The host record is a file, so a path whose last component is empty, "."
// or ".." does not name one. The check is purely syntactic: the
// configuration boundary must be able to refuse such a path without
// touching the filesystem, and the store owns the resolution of the path
// that the operator configured.
func hostRecordPathNamesFile(path string) bool {
	if path == "" {
		return false
	}
	if os.IsPathSeparator(path[len(path)-1]) {
		return false
	}
	switch filepath.Base(path) {
	case ".", "..":
		return false
	}
	return true
}
