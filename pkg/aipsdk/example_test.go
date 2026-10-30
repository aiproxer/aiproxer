package aipsdk_test

import (
	"fmt"

	"github.com/aiproxer/aiproxer/pkg/aipsdk"
)

func ExampleRequirement() {
	r := aipsdk.Requirement{Kind: aipsdk.PluginKindFrontend, ID: "openairesponses"}
	fmt.Println(string(r.Kind), r.ID)
	// Output: frontend openairesponses
}

func ExampleValidateRegistrations() {
	registrations := []aipsdk.Registration{
		{ID: "ui", Kind: aipsdk.PluginKindFrontend, Enabled: true},
	}
	required := []aipsdk.Requirement{
		{Kind: aipsdk.PluginKindFrontend, ID: "ui"},
	}
	if err := aipsdk.ValidateRegistrations(registrations, required); err != nil {
		fmt.Println("invalid:", err)
		return
	}
	fmt.Println("ok")
	// Output: ok
}
