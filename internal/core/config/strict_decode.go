package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// DefaultConfigMaxBytes is the shared upper bound for one configuration
// document. Filesystem adapters may apply a smaller startup-fixed limit.
const DefaultConfigMaxBytes int64 = 2 << 20 // 2 MiB

// LoadCategory is a bounded, secret-safe source/decode classification. Values
// identify failure classes only and never contain raw YAML or secrets.
type LoadCategory string

const (
	CategoryOK                LoadCategory = "ok"
	CategoryMissing           LoadCategory = "source_missing"
	CategoryEmpty             LoadCategory = "source_empty"
	CategoryWhitespace        LoadCategory = "source_whitespace"
	CategoryOversize          LoadCategory = "source_oversize"
	CategoryUnstable          LoadCategory = "source_unstable"
	CategoryNonAtomicUpdate   LoadCategory = "source_non_atomic_update"
	CategoryUnsupportedType   LoadCategory = "source_unsupported_type"
	CategoryMalformedYAML     LoadCategory = "decode_malformed_yaml"
	CategoryMultipleDocuments LoadCategory = "decode_multiple_documents"
	CategoryTrailingContent   LoadCategory = "decode_trailing_content"
	CategoryUnknownCoreField  LoadCategory = "decode_unknown_core_field"
	CategoryPartialUnreadable LoadCategory = "source_partial_unreadable"
)

// StrictDecode decodes exactly one YAML document into Config with KnownFields.
// Plugin-private yaml.Node subtrees are preserved. Failures are secret-safe.
func StrictDecode(raw []byte) (*Config, LoadCategory, error) {
	if cat := classifyConfigBytes(raw); cat != CategoryOK {
		return nil, cat, &LoadError{Category: cat}
	}

	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		msg := err.Error()
		switch {
		case strings.Contains(msg, "field") && strings.Contains(msg, "not found"):
			return nil, CategoryUnknownCoreField, &LoadError{Category: CategoryUnknownCoreField, err: err}
		case strings.Contains(msg, "unknown config key"):
			// Custom UnmarshalYAML unknown-key rejections (e.g. plugins.backend_discovery)
			// are classified with the unknown-field category and preserve the cause for
			// errors.Is / Error() inspection.
			return nil, CategoryUnknownCoreField, &LoadError{Category: CategoryUnknownCoreField, err: err}
		default:
			return nil, CategoryMalformedYAML, &LoadError{Category: CategoryMalformedYAML, err: err}
		}
	}

	var extra yaml.Node
	err := dec.Decode(&extra)
	switch {
	case errors.Is(err, io.EOF):
		return &cfg, CategoryOK, nil
	case err != nil:
		return nil, CategoryTrailingContent, &LoadError{Category: CategoryTrailingContent}
	default:
		return nil, CategoryMultipleDocuments, &LoadError{Category: CategoryMultipleDocuments}
	}
}

func classifyConfigBytes(raw []byte) LoadCategory {
	if int64(len(raw)) > DefaultConfigMaxBytes {
		return CategoryOversize
	}
	if len(raw) == 0 {
		return CategoryEmpty
	}
	for i := 0; i < len(raw); {
		r, size := utf8.DecodeRune(raw[i:])
		if r == utf8.RuneError && size == 1 {
			return CategoryOK
		}
		if !unicode.IsSpace(r) {
			return CategoryOK
		}
		i += size
	}
	return CategoryWhitespace
}

// LoadError is a secret-safe effective-load / decode failure.
type LoadError struct {
	Category LoadCategory
	err      error
}

func (e *LoadError) Error() string {
	if e == nil {
		return "config: unknown"
	}
	if e.err != nil {
		return fmt.Sprintf("config: %s: %v", e.Category, e.err)
	}
	return fmt.Sprintf("config: %s", e.Category)
}

func (e *LoadError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}
