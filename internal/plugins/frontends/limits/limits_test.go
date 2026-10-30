package limits

import (
	"testing"
)

func TestCount(t *testing.T) {
	t.Parallel()
	if err := Count("test", 5, 10); err != nil {
		t.Errorf("expected no error for got < max, got: %v", err)
	}
	if err := Count("test", 10, 10); err != nil {
		t.Errorf("expected no error for got == max, got: %v", err)
	}
	if err := Count("test", 11, 10); err == nil {
		t.Error("expected error for got > max, got nil")
	}
}

func TestBytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		field   string
		got     int
		max     int
		wantErr bool
		errMsg  string
	}{
		{
			name:    "under limit",
			field:   "payload",
			got:     50,
			max:     100,
			wantErr: false,
		},
		{
			name:    "exact limit",
			field:   "payload",
			got:     100,
			max:     100,
			wantErr: false,
		},
		{
			name:    "over limit",
			field:   "payload",
			got:     150,
			max:     100,
			wantErr: true,
			errMsg:  "payload has 150 bytes; maximum is 100",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := Bytes(tt.field, tt.got, tt.max)
			if (err != nil) != tt.wantErr {
				t.Errorf("Bytes() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr && err.Error() != tt.errMsg {
				t.Errorf("Bytes() error message = %q, want %q", err.Error(), tt.errMsg)
			}
		})
	}
}

func TestStringBytes_UnderLimit(t *testing.T) {
	t.Parallel()
	if err := StringBytes("field1", "hello", 10); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestStringBytes_ExactLimit(t *testing.T) {
	t.Parallel()
	if err := StringBytes("field1", "hello", 5); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestStringBytes_OverLimit(t *testing.T) {
	t.Parallel()
	err := StringBytes("field1", "hello world", 5)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	expected := "field1 has 11 bytes; maximum is 5"
	if err.Error() != expected {
		t.Fatalf("expected %q, got %q", expected, err.Error())
	}
}
