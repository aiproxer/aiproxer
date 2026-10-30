package backendplugin

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateFeatureMinorRequirements(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		host      map[string]Feature
		plugin    map[string]Feature
		minor     uint32
		wantErr   bool
		errSubstr string
	}{
		{
			name:    "empty maps with zero minor succeeds",
			host:    map[string]Feature{},
			plugin:  map[string]Feature{},
			minor:   0,
			wantErr: false,
		},
		{
			name:    "empty maps with arbitrary minor succeeds",
			host:    map[string]Feature{},
			plugin:  map[string]Feature{},
			minor:   4,
			wantErr: false,
		},
		{
			name: "cancellation handshake required in host below minor fails",
			host: map[string]Feature{
				FeatureCancellationHandshake: {Name: FeatureCancellationHandshake, Required: true},
			},
			plugin:    map[string]Feature{},
			minor:     ProtocolMinorCancellationHandshake - 1,
			wantErr:   true,
			errSubstr: "cancellation_handshake_v1 requires minor 8",
		},
		{
			name: "cancellation handshake required in plugin below minor fails",
			host: map[string]Feature{},
			plugin: map[string]Feature{
				FeatureCancellationHandshake: {Name: FeatureCancellationHandshake, Required: true},
			},
			minor:     ProtocolMinorCancellationHandshake - 1,
			wantErr:   true,
			errSubstr: "cancellation_handshake_v1 requires minor 8",
		},
		{
			name: "prompt cache residency required in host below minor fails",
			host: map[string]Feature{
				FeaturePromptCacheResidency: {Name: FeaturePromptCacheResidency, Required: true},
			},
			plugin:    map[string]Feature{},
			minor:     ProtocolMinorPromptCacheResidency - 1,
			wantErr:   true,
			errSubstr: "prompt_cache_residency_v1 requires minor 7",
		},
		{
			name: "prompt cache residency required in plugin below minor fails",
			host: map[string]Feature{},
			plugin: map[string]Feature{
				FeaturePromptCacheResidency: {Name: FeaturePromptCacheResidency, Required: true},
			},
			minor:     ProtocolMinorPromptCacheResidency - 1,
			wantErr:   true,
			errSubstr: "prompt_cache_residency_v1 requires minor 7",
		},
		{
			name: "semantic extensions required in host below minor fails",
			host: map[string]Feature{
				FeatureSemanticExtensions: {Name: FeatureSemanticExtensions, Required: true},
			},
			plugin:    map[string]Feature{},
			minor:     ProtocolMinorSemanticExtensions - 1,
			wantErr:   true,
			errSubstr: "semantic_extensions_v1 requires minor 6",
		},
		{
			name: "semantic extensions required in plugin below minor fails",
			host: map[string]Feature{},
			plugin: map[string]Feature{
				FeatureSemanticExtensions: {Name: FeatureSemanticExtensions, Required: true},
			},
			minor:     ProtocolMinorSemanticExtensions - 1,
			wantErr:   true,
			errSubstr: "semantic_extensions_v1 requires minor 6",
		},
		{
			name: "optional anywhere with low minor succeeds",
			host: map[string]Feature{
				FeatureCancellationHandshake: {Name: FeatureCancellationHandshake, Required: false},
				FeaturePromptCacheResidency:  {Name: FeaturePromptCacheResidency, Required: false},
				FeatureSemanticExtensions:    {Name: FeatureSemanticExtensions, Required: false},
			},
			plugin: map[string]Feature{
				FeatureCancellationHandshake: {Name: FeatureCancellationHandshake, Required: false},
				FeaturePromptCacheResidency:  {Name: FeaturePromptCacheResidency, Required: false},
				FeatureSemanticExtensions:    {Name: FeatureSemanticExtensions, Required: false},
			},
			minor:   0,
			wantErr: false,
		},
		{
			name: "all known features required with max minor succeeds",
			host: map[string]Feature{
				FeatureCancellationHandshake: {Name: FeatureCancellationHandshake, Required: true},
				FeaturePromptCacheResidency:  {Name: FeaturePromptCacheResidency, Required: true},
				FeatureSemanticExtensions:    {Name: FeatureSemanticExtensions, Required: true},
			},
			plugin: map[string]Feature{
				FeatureCancellationHandshake: {Name: FeatureCancellationHandshake, Required: true},
				FeaturePromptCacheResidency:  {Name: FeaturePromptCacheResidency, Required: true},
				FeatureSemanticExtensions:    {Name: FeatureSemanticExtensions, Required: true},
			},
			minor:   ProtocolMinorCancellationHandshake,
			wantErr: false,
		},
		{
			name: "precedence pin: both cancellation and semantic extensions required with minor below semantic extensions",
			host: map[string]Feature{
				FeatureCancellationHandshake: {Name: FeatureCancellationHandshake, Required: true},
				FeatureSemanticExtensions:    {Name: FeatureSemanticExtensions, Required: true},
			},
			plugin: map[string]Feature{
				FeatureCancellationHandshake: {Name: FeatureCancellationHandshake, Required: true},
				FeatureSemanticExtensions:    {Name: FeatureSemanticExtensions, Required: true},
			},
			minor:     ProtocolMinorSemanticExtensions - 1,
			wantErr:   true,
			errSubstr: "cancellation_handshake_v1 requires minor 8",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateFeatureMinorRequirements(tt.host, tt.plugin, tt.minor)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateFeatureMinorRequirements() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				if !errors.Is(err, ErrIncompatibleMinor) {
					t.Fatalf("expected ErrIncompatibleMinor in error chain, got %v", err)
				}
				if !strings.Contains(err.Error(), tt.errSubstr) {
					t.Fatalf("expected error string to contain %q, got %q", tt.errSubstr, err.Error())
				}
			}
		})
	}
}

func TestFeatureMinimumMinor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		feature   string
		wantMinor uint32
		wantOk    bool
	}{
		{
			name:      "cancellation handshake",
			feature:   FeatureCancellationHandshake,
			wantMinor: ProtocolMinorCancellationHandshake,
			wantOk:    true,
		},
		{
			name:      "prompt cache residency",
			feature:   FeaturePromptCacheResidency,
			wantMinor: ProtocolMinorPromptCacheResidency,
			wantOk:    true,
		},
		{
			name:      "semantic extensions",
			feature:   FeatureSemanticExtensions,
			wantMinor: ProtocolMinorSemanticExtensions,
			wantOk:    true,
		},
		{
			name:      "count tokens unknown feature",
			feature:   "count_tokens",
			wantMinor: 0,
			wantOk:    false,
		},
		{
			name:      "arbitrary unknown feature",
			feature:   "unknown_feature",
			wantMinor: 0,
			wantOk:    false,
		},
		{
			name:      "empty feature string",
			feature:   "",
			wantMinor: 0,
			wantOk:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotMinor, gotOk := featureMinimumMinor(tt.feature)
			if gotMinor != tt.wantMinor || gotOk != tt.wantOk {
				t.Fatalf("featureMinimumMinor(%q) = (%d, %t), want (%d, %t)", tt.feature, gotMinor, gotOk, tt.wantMinor, tt.wantOk)
			}
		})
	}
}
