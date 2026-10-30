package featurehost_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/aiproxer/aiproxer/pkg/aipsdk/featurehost"
)

type mockBinding struct {
	id       string
	validErr error
}

func (m mockBinding) HostBindingID() string {
	return m.id
}

func (m mockBinding) ValidateHostBinding() error {
	return m.validErr
}

type typedNilMockBinding struct{}

func (m *typedNilMockBinding) HostBindingID() string {
	if m == nil {
		return "typed-nil"
	}
	return "not-nil"
}

func (m *typedNilMockBinding) ValidateHostBinding() error {
	if m == nil {
		return errors.New("typed-nil receiver error")
	}
	return nil
}

func TestValidate_EmptyAndNilSlice_Succeeds(t *testing.T) {
	t.Parallel()

	if err := featurehost.Validate(nil); err != nil {
		t.Fatalf("Validate(nil) unexpected error: %v", err)
	}
	if err := featurehost.Validate([]featurehost.Registration{}); err != nil {
		t.Fatalf("Validate(empty) unexpected error: %v", err)
	}
}

func TestValidate_NilBinding_Fails(t *testing.T) {
	t.Parallel()

	regs := []featurehost.Registration{
		{Binding: nil},
	}
	err := featurehost.Validate(regs)
	if err == nil {
		t.Fatal("Validate with nil Binding expected error, got nil")
	}
	if !errors.Is(err, featurehost.ErrNilBinding) {
		t.Fatalf("expected ErrNilBinding, got: %v", err)
	}
}

func TestValidate_EmptyBindingID_Fails(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		id   string
	}{
		{name: "empty string", id: ""},
		{name: "spaces only", id: "   "},
		{name: "tabs and newlines", id: "\t\n "},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			regs := []featurehost.Registration{
				{Binding: mockBinding{id: tc.id}},
			}
			err := featurehost.Validate(regs)
			if err == nil {
				t.Fatal("expected error for empty binding ID, got nil")
			}
			if !errors.Is(err, featurehost.ErrEmptyBindingID) {
				t.Fatalf("expected ErrEmptyBindingID, got: %v", err)
			}
		})
	}
}

func TestValidate_BindingIDTooLong_Fails(t *testing.T) {
	t.Parallel()

	longID := strings.Repeat("a", featurehost.MaxBindingIDLength+1)
	regs := []featurehost.Registration{
		{Binding: mockBinding{id: longID}},
	}
	err := featurehost.Validate(regs)
	if err == nil {
		t.Fatal("expected error for too long binding ID, got nil")
	}
	if !errors.Is(err, featurehost.ErrBindingIDTooLong) {
		t.Fatalf("expected ErrBindingIDTooLong, got: %v", err)
	}
}

func TestValidate_InvalidBinding_Fails(t *testing.T) {
	t.Parallel()

	customErr := errors.New("validation failed for business logic")
	regs := []featurehost.Registration{
		{Binding: mockBinding{id: "valid-id", validErr: customErr}},
	}
	err := featurehost.Validate(regs)
	if err == nil {
		t.Fatal("expected error for invalid binding, got nil")
	}
	if !errors.Is(err, featurehost.ErrInvalidBinding) {
		t.Fatalf("expected ErrInvalidBinding, got: %v", err)
	}
	if !errors.Is(err, customErr) {
		t.Fatalf("expected underlying error wrapped, got: %v", err)
	}
}

func TestValidate_TypedNilReceiver_FailsDeterministically(t *testing.T) {
	t.Parallel()

	var typedNil *typedNilMockBinding
	regs := []featurehost.Registration{
		{Binding: typedNil},
	}
	err := featurehost.Validate(regs)
	if err == nil {
		t.Fatal("expected error for typed-nil binding, got nil")
	}
	if !errors.Is(err, featurehost.ErrInvalidBinding) {
		t.Fatalf("expected ErrInvalidBinding, got: %v", err)
	}
}

func TestValidate_DuplicateBindingID_Fails(t *testing.T) {
	t.Parallel()

	regs := []featurehost.Registration{
		{Binding: mockBinding{id: "binding-alpha"}},
		{Binding: mockBinding{id: "binding-beta"}},
		{Binding: mockBinding{id: "binding-alpha"}},
	}
	err := featurehost.Validate(regs)
	if err == nil {
		t.Fatal("expected error for duplicate binding ID, got nil")
	}
	if !errors.Is(err, featurehost.ErrDuplicateBinding) {
		t.Fatalf("expected ErrDuplicateBinding, got: %v", err)
	}
	if !strings.Contains(err.Error(), "binding-alpha") {
		t.Fatalf("expected error message to contain duplicate id 'binding-alpha', got: %s", err.Error())
	}
}

func TestValidate_DuplicateBindingID_TrimmedMatch_Fails(t *testing.T) {
	t.Parallel()

	regs := []featurehost.Registration{
		{Binding: mockBinding{id: "binding-alpha"}},
		{Binding: mockBinding{id: "  binding-alpha  "}},
	}
	err := featurehost.Validate(regs)
	if err == nil {
		t.Fatal("expected error for duplicate trimmed binding ID, got nil")
	}
	if !errors.Is(err, featurehost.ErrDuplicateBinding) {
		t.Fatalf("expected ErrDuplicateBinding, got: %v", err)
	}
}

func TestValidate_MultipleValid_Succeeds(t *testing.T) {
	t.Parallel()

	regs := []featurehost.Registration{
		{Binding: mockBinding{id: "binding-alpha"}},
		{Binding: mockBinding{id: "binding-beta"}},
		{Binding: mockBinding{id: "binding-gamma"}},
	}
	if err := featurehost.Validate(regs); err != nil {
		t.Fatalf("expected success for distinct valid bindings, got: %v", err)
	}
}

func TestValidate_DefensiveSliceHandling(t *testing.T) {
	t.Parallel()

	original := []featurehost.Registration{
		{Binding: mockBinding{id: "binding-1"}},
		{Binding: mockBinding{id: "binding-2"}},
	}
	copyRegs := make([]featurehost.Registration, len(original))
	copy(copyRegs, original)

	if err := featurehost.Validate(original); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}

	for i := range original {
		if original[i].Binding.HostBindingID() != copyRegs[i].Binding.HostBindingID() {
			t.Fatalf("original slice was modified at index %d", i)
		}
	}
}
