package storecontract_test

import (
	"testing"

	"github.com/aiproxer/aiproxer/internal/core/securesession/adapters/memory"
	"github.com/aiproxer/aiproxer/internal/core/securesession/app"
	"github.com/aiproxer/aiproxer/internal/core/securesession/storecontract"
)

func TestStoreContract_Memory(t *testing.T) {
	t.Parallel()
	storecontract.RunAll(t, func(*testing.T) app.Store {
		return memory.New(memory.Options{})
	})
}
