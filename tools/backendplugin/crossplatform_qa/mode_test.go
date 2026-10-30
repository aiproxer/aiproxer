package main

import "testing"

func TestQAMode_RejectsModesWithoutCompileOwner(t *testing.T) {
	t.Parallel()
	if err := validateQAMode(true, true); err == nil {
		t.Fatal("compile-only plus skip-compile would produce no compilation evidence")
	}
	for _, mode := range [][2]bool{{false, false}, {true, false}, {false, true}} {
		if err := validateQAMode(mode[0], mode[1]); err != nil {
			t.Errorf("valid mode %v: %v", mode, err)
		}
	}
}
