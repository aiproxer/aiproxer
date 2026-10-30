package extensions

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/session"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/sessionclassification"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/workspace"
)

type sessionClassificationClassifierFunc func(context.Context, sessionclassification.Input) (session.Classification, error)

func (f sessionClassificationClassifierFunc) ID() string {
	panic("classifier ID must not be called during request evaluation")
}

func (f sessionClassificationClassifierFunc) Classify(ctx context.Context, in sessionclassification.Input) (session.Classification, error) {
	return f(ctx, in)
}

func TestRunSessionClassificationStage_NoPlaneNoop(t *testing.T) {
	t.Parallel()

	want := codingAgentClassification("kept", 8)
	in := sessionclassification.Input{Session: session.SessionView{Classification: want}}

	if got := RunSessionClassificationStage(t.Context(), nil, nil, in); got != want {
		t.Fatalf("classification = %+v, want unchanged %+v", got, want)
	}
}

func TestRunSessionClassificationStage_ProjectsValidPositive(t *testing.T) {
	t.Parallel()

	want := codingAgentClassification("client.identity", 1)
	classifier := sessionClassificationClassifierFunc(func(context.Context, sessionclassification.Input) (session.Classification, error) {
		return want, nil
	})

	got := RunSessionClassificationStage(t.Context(), nil, classifier, sessionclassification.Input{})
	if got != want {
		t.Fatalf("classification = %+v, want %+v", got, want)
	}
}

func TestRunSessionClassificationStage_InvalidOutputFailsOpen(t *testing.T) {
	t.Parallel()

	classifier := sessionClassificationClassifierFunc(func(context.Context, sessionclassification.Input) (session.Classification, error) {
		return session.Classification{Kind: session.KindCodingAgent}, nil
	})

	if got := RunSessionClassificationStage(t.Context(), nil, classifier, sessionclassification.Input{}); got != (session.Classification{}) {
		t.Fatalf("classification = %+v, want unknown", got)
	}
}

func TestRunSessionClassificationStage_ClassifierErrorFailsOpenWithoutLoggingError(t *testing.T) {
	t.Parallel()

	const secretError = "raw client token: bearer abc123"
	var logOutput bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logOutput, nil))
	classifier := sessionClassificationClassifierFunc(func(context.Context, sessionclassification.Input) (session.Classification, error) {
		return session.Classification{}, errors.New(secretError)
	})

	if got := RunSessionClassificationStage(t.Context(), logger, classifier, sessionclassification.Input{}); got != (session.Classification{}) {
		t.Fatalf("classification = %+v, want unknown", got)
	}
	if strings.Contains(logOutput.String(), secretError) {
		t.Fatalf("classification error leaked into logs: %q", logOutput.String())
	}
	if !strings.Contains(logOutput.String(), "classifier_error") {
		t.Fatalf("logs do not contain a bounded failure code: %q", logOutput.String())
	}
}

func TestRunSessionClassificationStage_PanicFailsOpenWithoutLoggingPanicValue(t *testing.T) {
	t.Parallel()

	const secretPanic = "prompt excerpt with secret api-key"
	var logOutput bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logOutput, nil))
	classifier := sessionClassificationClassifierFunc(func(context.Context, sessionclassification.Input) (session.Classification, error) {
		panic(secretPanic)
	})

	if got := RunSessionClassificationStage(t.Context(), logger, classifier, sessionclassification.Input{}); got != (session.Classification{}) {
		t.Fatalf("classification = %+v, want unknown", got)
	}
	if strings.Contains(logOutput.String(), secretPanic) {
		t.Fatalf("panic value leaked into logs: %q", logOutput.String())
	}
	if !strings.Contains(logOutput.String(), "classifier_error") {
		t.Fatalf("logs do not contain a bounded failure code: %q", logOutput.String())
	}
}

func TestRunSessionClassificationStage_ContextCancellation(t *testing.T) {
	t.Parallel()

	t.Run("already canceled context skips classifier", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		called := false
		classifier := sessionClassificationClassifierFunc(func(context.Context, sessionclassification.Input) (session.Classification, error) {
			called = true
			return codingAgentClassification("too_late", 1), nil
		})

		if got := RunSessionClassificationStage(ctx, nil, classifier, sessionclassification.Input{}); got != (session.Classification{}) {
			t.Fatalf("classification = %+v, want unknown", got)
		}
		if called {
			t.Fatal("classifier was called after context cancellation")
		}
	})

	t.Run("cancellation during callback discards result", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		classifier := sessionClassificationClassifierFunc(func(context.Context, sessionclassification.Input) (session.Classification, error) {
			cancel()
			return codingAgentClassification("canceled", 1), nil
		})

		if got := RunSessionClassificationStage(ctx, nil, classifier, sessionclassification.Input{}); got != (session.Classification{}) {
			t.Fatalf("classification = %+v, want unknown after cancellation", got)
		}
	})

	t.Run("prior positive survives cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		want := codingAgentClassification("prior", 4)
		called := false
		classifier := sessionClassificationClassifierFunc(func(context.Context, sessionclassification.Input) (session.Classification, error) {
			called = true
			return session.Classification{}, nil
		})
		in := sessionclassification.Input{Session: session.SessionView{Classification: want}}

		if got := RunSessionClassificationStage(ctx, nil, classifier, in); got != want {
			t.Fatalf("classification = %+v, want prior positive %+v", got, want)
		}
		if called {
			t.Fatal("classifier was called after a prior positive classification was available")
		}
	})
}

func TestRunSessionClassificationStage_PriorPositiveIsImmutable(t *testing.T) {
	t.Parallel()

	want := codingAgentClassification("prior", 4)
	later := codingAgentClassification("later", 9)
	called := false
	classifier := sessionClassificationClassifierFunc(func(context.Context, sessionclassification.Input) (session.Classification, error) {
		called = true
		return later, nil
	})
	in := sessionclassification.Input{Session: session.SessionView{Classification: want}}

	if got := RunSessionClassificationStage(t.Context(), nil, classifier, in); got != want {
		t.Fatalf("classification = %+v, want prior positive %+v", got, want)
	}
	if called {
		t.Fatal("classifier ran after the session already had a positive classification")
	}
}

func TestRunSessionClassificationStage_ClonesMetadataBeforeCallback(t *testing.T) {
	t.Parallel()

	in := sessionclassification.Input{
		Session: session.SessionView{Labels: map[string]string{"source": "caller"}},
		Workspace: workspace.WorkspaceView{
			Markers: []string{"caller-marker"},
			Labels:  map[string]string{"workspace": "caller"},
		},
	}
	classifier := sessionClassificationClassifierFunc(func(_ context.Context, callbackInput sessionclassification.Input) (session.Classification, error) {
		callbackInput.Session.Labels["source"] = "classifier"
		callbackInput.Workspace.Markers[0] = "classifier-marker"
		callbackInput.Workspace.Labels["workspace"] = "classifier"
		return session.Classification{}, nil
	})

	_ = RunSessionClassificationStage(t.Context(), nil, classifier, in)
	if got := in.Session.Labels["source"]; got != "caller" {
		t.Fatalf("caller session label changed to %q", got)
	}
	if got := in.Workspace.Markers[0]; got != "caller-marker" {
		t.Fatalf("caller workspace marker changed to %q", got)
	}
	if got := in.Workspace.Labels["workspace"]; got != "caller" {
		t.Fatalf("caller workspace label changed to %q", got)
	}
}

func codingAgentClassification(evidence session.EvidenceCode, revision uint64) session.Classification {
	return session.Classification{
		Kind:       session.KindCodingAgent,
		Source:     session.SourceLocalIdentity,
		Confidence: session.ConfidenceHigh,
		Evidence:   evidence,
		Revision:   revision,
	}
}
