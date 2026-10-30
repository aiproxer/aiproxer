package secretguardhost_test

import (
	"errors"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/featurehost"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/secretguardhost"
)

func TestBinding_ImplementsFeatureHostBinding(t *testing.T) {
	t.Parallel()
	var _ featurehost.Binding = (*secretguardhost.Binding)(nil)

	b := &secretguardhost.Binding{
		SingleUser: secretguardhost.SingleUserOptions{
			IncludePopularEnv: true,
			IncludeEnv:        []string{"TEST_KEY"},
		},
	}
	if b.HostBindingID() != secretguardhost.BindingID {
		t.Fatalf("HostBindingID: got %q, want %q", b.HostBindingID(), secretguardhost.BindingID)
	}
	if err := b.ValidateHostBinding(); err != nil {
		t.Fatalf("ValidateHostBinding: %v", err)
	}
	reg := b.Registration()
	if reg.Binding != b {
		t.Fatalf("Registration: expected binding identity preserved")
	}
}

func TestBinding_NilReceiverValidation(t *testing.T) {
	t.Parallel()
	var b *secretguardhost.Binding
	if b.HostBindingID() != secretguardhost.BindingID {
		t.Fatalf("HostBindingID on nil: got %q, want %q", b.HostBindingID(), secretguardhost.BindingID)
	}
	err := b.ValidateHostBinding()
	if !errors.Is(err, secretguardhost.ErrNilBinding) {
		t.Fatalf("ValidateHostBinding on nil: got %v, want ErrNilBinding", err)
	}
}
