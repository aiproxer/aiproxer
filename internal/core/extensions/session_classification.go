package extensions

import (
	"context"
	"log/slog"
	"maps"
	"slices"

	"github.com/aiproxer/aiproxer/internal/core/safety"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
)

// RunSessionClassificationStage evaluates the optional SDK classifier and
// returns a validated projection. An existing positive classification is immutable.
func RunSessionClassificationStage(ctx context.Context, log *slog.Logger, classifier sessionclassification.Classifier, in sessionclassification.Input) session.Classification {
	current := in.Session.Classification
	if current.IsCodingAgent() {
		return current
	}
	if classifier == nil {
		return current
	}
	if ctx == nil {
		return session.Classification{}
	}
	if ctx.Err() != nil {
		logSessionClassificationFailure(ctx, log, "context_canceled")
		return session.Classification{}
	}

	result, err := safety.CallValue(safety.BoundaryExtension, "session_classification", func() (session.Classification, error) {
		return classifier.Classify(ctx, cloneSessionClassificationInput(in))
	})
	if ctx.Err() != nil {
		logSessionClassificationFailure(ctx, log, "context_canceled")
		return session.Classification{}
	}
	if err != nil {
		logSessionClassificationFailure(ctx, log, "classifier_error")
		return session.Classification{}
	}
	if err := result.Validate(); err != nil {
		logSessionClassificationFailure(ctx, log, "invalid_output")
		return session.Classification{}
	}
	return result
}

func cloneSessionClassificationInput(in sessionclassification.Input) sessionclassification.Input {
	in.Session.Labels = maps.Clone(in.Session.Labels)
	in.Workspace.Markers = slices.Clone(in.Workspace.Markers)
	in.Workspace.Labels = maps.Clone(in.Workspace.Labels)
	return in
}

func logSessionClassificationFailure(ctx context.Context, log *slog.Logger, code string) {
	if ctx == nil || log == nil {
		return
	}
	log.LogAttrs(ctx, slog.LevelWarn, "extensions: session classification failed open", slog.String("failure_code", code))
}
