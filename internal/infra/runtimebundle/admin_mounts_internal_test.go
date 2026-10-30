package runtimebundle

import (
	"net/http"
	"testing"
)

// TestAdminMountsForGeneration_RegisteredMounts covers the generic protected
// mount projection: the process-boundary snapshot is cloned for each
// generation, and the open-source runtime adds no billing mount of its own.
func TestAdminMountsForGeneration_RegisteredMounts(t *testing.T) {
	t.Parallel()
	callerHandler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})

	cand := &candidateAssembly{}
	cand.process.ps = &ProcessServices{}
	cand.process.ps.opts = &BuildOptions{Production: ProductionOptions{
		AdminMounts: []AdminMount{{Path: "/admin/enterprise", Handler: callerHandler}},
	}}

	mounts := adminMountsForGeneration(cand)
	if len(mounts) != 1 || mounts[0].Path != "/admin/enterprise" || mounts[0].Handler == nil {
		t.Fatalf("mounts=%+v want only the registered caller mount", mounts)
	}

	withoutOpts := &candidateAssembly{process: candidateProcessRefs{ps: &ProcessServices{}}}
	if mounts := adminMountsForGeneration(withoutOpts); mounts != nil {
		t.Fatalf("mounts without process options=%+v want nil", mounts)
	}
	if mounts := adminMountsForGeneration(nil); mounts != nil {
		t.Fatalf("nil candidate mounts=%+v want nil", mounts)
	}
}
