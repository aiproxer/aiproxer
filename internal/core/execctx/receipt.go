package execctx

// ExecutionReceipt is the neutral, immutable identity of the host billing
// operation one auxiliary execution was admitted as. It carries no account,
// money, provider or content field: StoreID and BillingCallID are host-stamped
// opaque references, and the scope/role are the trusted values the runtime
// observed for the request.
//
// It lives in trusted internal context only. It is never copied into a
// canonical call, a public auxiliary request or a wire payload, so a client
// cannot select or spoof a linked operation. An allocated identity alone does
// not prove admission, payable usage or settlement; the host's durable terminal
// records prove those facts.
type ExecutionReceipt struct {
	StoreID       string
	BillingCallID string
	TenantID      string
	PrincipalID   string
	Role          string
}

// Available reports whether the receipt names a complete host operation
// reference. A missing store or call identity is an explicit unavailable link,
// never a fabricated one.
func (r ExecutionReceipt) Available() bool {
	return r.StoreID != "" && r.BillingCallID != ""
}
