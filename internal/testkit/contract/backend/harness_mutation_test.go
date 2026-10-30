package backend

import (
	"context"
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/internal/testkit/contract/semantic"
)

func TestCertifyBackendRejectsNilHarness(t *testing.T) {
	_, err := CertifyBackend(context.Background(), nil)
	if err == nil {
		t.Fatal("expected nil BackendHarness to fail")
	}
}

func TestCertifyBackendRejectsIncompatibleSubjectKind(t *testing.T) {
	h := selectionBackendHarness{subject: semantic.SubjectDescriptor{ID: "frontend", Kind: semantic.KindFrontend}}
	_, err := CertifyBackend(context.Background(), h)
	if err == nil {
		t.Fatal("expected incompatible SubjectKind to fail")
	}
}

type constructionErrorHarness struct{ selectionBackendHarness }

func (constructionErrorHarness) Backend(context.Context) (BackendView, error) {
	return nil, errors.New("construction failed")
}

func TestCertifyBackendRejectsConstructionError(t *testing.T) {
	h := constructionErrorHarness{selectionBackendHarness{subject: semantic.SubjectDescriptor{ID: "backend", Kind: semantic.KindBackendFamily}}}
	_, err := CertifyBackend(context.Background(), h)
	if err == nil {
		t.Fatal("expected backend construction error to fail")
	}
}
