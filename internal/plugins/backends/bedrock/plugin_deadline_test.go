package bedrock

import (
	"context"
	"testing"
	"time"
)

func TestEnsureLoadConfigDeadline_todoYieldsDeadline(t *testing.T) {
	t.Parallel()
	c, cancel := ensureLoadConfigDeadline(context.TODO())
	defer cancel()
	if _, ok := c.Deadline(); !ok {
		t.Fatal("expected child deadline when parent has none")
	}
}

func TestEnsureLoadConfigDeadline_nilYieldsDeadline(t *testing.T) {
	t.Parallel()
	c, cancel := ensureLoadConfigDeadline(nil) //nolint:staticcheck // nil ctx: ensureLoadConfigDeadline must coerce to a usable deadline context
	defer cancel()
	if _, ok := c.Deadline(); !ok {
		t.Fatal("expected child deadline when parent is nil")
	}
}

func TestEnsureLoadConfigDeadline_backgroundYieldsDeadline(t *testing.T) {
	t.Parallel()
	c, cancel := ensureLoadConfigDeadline(context.Background())
	defer cancel()
	if _, ok := c.Deadline(); !ok {
		t.Fatal("expected child deadline when parent has none")
	}
}

func TestEnsureLoadConfigDeadline_preservesExistingDeadline(t *testing.T) {
	t.Parallel()
	parent, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	c, childCancel := ensureLoadConfigDeadline(parent)
	defer childCancel()
	if c != parent {
		t.Fatal("expected same context when parent already has a deadline")
	}
}
