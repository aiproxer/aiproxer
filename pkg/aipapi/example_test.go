package aipapi_test

import (
	"context"
	"fmt"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
)

func ExampleTextPart() {
	p := aipapi.TextPart("hello")
	fmt.Println(string(p.Kind), p.Text)
	// Output: text hello
}

func ExampleNewFixedEventStream() {
	ctx := context.Background()
	st := aipapi.NewFixedEventStream([]aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: "hi"},
		{Kind: aipapi.EventResponseFinished},
	})
	out, err := aipapi.Collect(ctx, st)
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(out.Text.String())
	// Output: hi
}

func ExampleCall_Validate() {
	c := aipapi.Call{
		ID: "call-1",
		Messages: []aipapi.Message{{
			Role:  aipapi.RoleUser,
			Parts: []aipapi.Part{aipapi.TextPart("ping")},
		}},
	}
	if err := c.Validate(); err != nil {
		fmt.Println("invalid:", err)
		return
	}
	fmt.Println("valid")
	// Output: valid
}

func ExampleNewBackendCaps() {
	caps := aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools)
	_, hasStream := caps[aipapi.CapabilityStreaming]
	_, hasVision := caps[aipapi.CapabilityVision]
	fmt.Println(hasStream, hasVision)
	// Output: true false
}
