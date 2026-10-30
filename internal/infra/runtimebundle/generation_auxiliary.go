package runtimebundle

import (
	"fmt"
	"reflect"

	"github.com/aiproxer/aiproxer/internal/infra/auxiliary"
	sdkauxiliary "github.com/aiproxer/aiproxer/pkg/aipsdk/auxiliary"
)

func newGenerationAuxiliaryRunner(ps *ProcessServices) (*auxiliary.GenerationExecutorRunner, sdkauxiliary.BackgroundClient, sdkauxiliary.BackgroundPoller, error) {
	genRunner := auxiliary.NewGenerationExecutorRunner()
	if ps == nil || isNilAuxiliaryCapability(ps.BackgroundAux) {
		return genRunner, nil, nil, nil
	}
	boundClient := ps.BackgroundAux.BindRunner(genRunner)
	poller, ok := boundClient.(sdkauxiliary.BackgroundPoller)
	if !ok {
		return nil, nil, nil, fmt.Errorf("runtimebundle: background scheduler bound client does not implement poller")
	}
	return genRunner, boundClient, poller, nil
}

func isNilAuxiliaryCapability(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
		return rv.IsNil()
	default:
		return false
	}
}
