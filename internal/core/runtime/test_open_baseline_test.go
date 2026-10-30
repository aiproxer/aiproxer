package runtime

import "github.com/aiproxer/aiproxer/pkg/aipapi"

func testMinimalUserMessages() []aipapi.Message {
	return []aipapi.Message{{
		Role:  aipapi.RoleUser,
		Parts: []aipapi.Part{aipapi.TextPart("hi")},
	}}
}
