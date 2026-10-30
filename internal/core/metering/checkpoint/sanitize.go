package checkpoint

import "github.com/aiproxer/aiproxer/pkg/aipapi"

// SanitizeCall clears resume secrets and other bearer material from a Call clone
// so memory-held metering snapshots never retain raw resume tokens (req 2.7).
func SanitizeCall(c aipapi.Call) aipapi.Call {
	c.Session.ResumeToken = ""
	return c
}
