package configreload

import (
	"errors"
	"strings"

	"github.com/aiproxer/aiproxer/internal/core/config"
	sdkreload "github.com/aiproxer/aiproxer/pkg/aipsdk/configreload"
)

// MapLoadFailure maps effective-load / source-integrity failures to a terminal
// result category and bounded reason (req 2.10, 3.10). Values are never included.
func MapLoadFailure(err error) (sdkreload.ResultCategory, string) {
	if err == nil {
		return sdkreload.ResultInternalFailed, "unknown"
	}
	if _, ok := errors.AsType[*RestartRequiredError](err); ok {
		return sdkreload.ResultRestartRequired, StageClassify
	}
	var le *config.LoadError
	if errors.As(err, &le) && le != nil && le.Category != "" {
		return mapLoadCategory(string(le.Category))
	}
	// configsource.IntegrityError and similar expose Category via Error() prefix
	// or errors.As; callers may also pass CategoryOf results through ReasonCategory.
	msg := err.Error()
	switch {
	case strings.Contains(msg, string(config.CategoryMissing)),
		strings.Contains(msg, string(config.CategoryEmpty)),
		strings.Contains(msg, string(config.CategoryWhitespace)),
		strings.Contains(msg, string(config.CategoryOversize)),
		strings.Contains(msg, string(config.CategoryUnstable)),
		strings.Contains(msg, string(config.CategoryNonAtomicUpdate)),
		strings.Contains(msg, string(config.CategoryUnsupportedType)),
		strings.Contains(msg, string(config.CategoryPartialUnreadable)):
		return sdkreload.ResultSourceIntegrity, StageRead
	case strings.Contains(msg, string(config.CategoryMalformedYAML)),
		strings.Contains(msg, string(config.CategoryMultipleDocuments)),
		strings.Contains(msg, string(config.CategoryTrailingContent)),
		strings.Contains(msg, string(config.CategoryUnknownCoreField)):
		return sdkreload.ResultInvalid, StageLoad
	case strings.Contains(msg, "validate"):
		return sdkreload.ResultInvalid, StageLoad
	default:
		return sdkreload.ResultInvalid, StageLoad
	}
}

func mapLoadCategory(cat string) (sdkreload.ResultCategory, string) {
	switch config.LoadCategory(cat) {
	case config.CategoryMissing, config.CategoryEmpty, config.CategoryWhitespace,
		config.CategoryOversize, config.CategoryUnstable, config.CategoryNonAtomicUpdate,
		config.CategoryUnsupportedType, config.CategoryPartialUnreadable:
		return sdkreload.ResultSourceIntegrity, StageRead
	case config.CategoryMalformedYAML, config.CategoryMultipleDocuments,
		config.CategoryTrailingContent, config.CategoryUnknownCoreField:
		return sdkreload.ResultInvalid, StageLoad
	default:
		return sdkreload.ResultInvalid, StageLoad
	}
}

// MapLoadCategory maps a known LoadCategory string to a terminal result.
func MapLoadCategory(cat string) (sdkreload.ResultCategory, string) {
	if cat == "" {
		return sdkreload.ResultInternalFailed, "unknown"
	}
	return mapLoadCategory(cat)
}
