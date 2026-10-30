package backendplugin

import "context"

// Service is the public plugin authoring entrypoint before instance configuration.
type Service interface {
	// Describe returns plugin identity and factory exports.
	Describe(ctx context.Context) (PluginDescriptor, error)
	// Configure creates a configured instance after negotiation and peer authentication.
	Configure(ctx context.Context, req ConfigureRequest) (ConfiguredInstance, error)
}

// ConfiguredInstance is one configured backend factory instance.
type ConfiguredInstance interface {
	// Resolve returns static or model-aware capability profiles.
	Resolve(ctx context.Context, modelID *string) (ResolvedProfile, error)
	// ListModels returns a bounded inventory snapshot when advertised.
	ListModels(ctx context.Context, maxModels uint32) (ListModelsResponse, error)
	// Execute runs one bidirectional attempt stream.
	Execute(stream ExecuteStream) error
	// Close releases instance resources.
	Close(ctx context.Context) error
}

// TokenCounter is an optional advertised counting capability.
type TokenCounter interface {
	// CountTokens estimates tokens for an invocation.
	CountTokens(ctx context.Context, req CountTokensRequest) (CountTokensResponse, error)
}

// BillingFinalizer is an optional advertised billing finalization capability.
type BillingFinalizer interface {
	// FinalizeBilling finalizes billing for an attempt lineage idempotently.
	FinalizeBilling(ctx context.Context, req FinalizeBillingRequest) (FinalizeBillingResponse, error)
}

// TerminalError is implemented by a connector failure that can supply the
// host-visible terminal classification before ForwardExecute publishes the
// terminal frame. The returned PluginError must be bounded and secret-free;
// provider text and credentials never belong in it. When output has already
// been committed, ForwardExecute forces Retryable=false and OutputCommitted=true
// regardless of the returned value.
type TerminalError interface {
	error
	TerminalPluginError() *PluginError
}

// ExecuteStream is the public bidirectional execute abstraction using DTOs only.
// Recv must return when Context is cancelled. Implementations that can block in
// Recv should also implement the optional interface:
//
// OptionalExecuteStreamCloser is an optional stream interface for unblocking Recv.
type OptionalExecuteStreamCloser interface {
	Close() error
}

// OptionalNegotiatedStream is an optional ExecuteStream extension for inspecting
// the negotiated protocol capabilities.
type OptionalNegotiatedStream interface {
	Negotiation() Negotiation
}

// Session.Execute uses that optional close seam to unblock caller-owned streams
// before joining its client-to-server pump. For legacy streams without Close,
// the caller owns cancellation of Context; Session cannot cancel an arbitrary
// caller-owned context without changing this public interface. Such streams
// must therefore return from Recv when their Context is cancelled.
type ExecuteStream interface {
	// Context is the stream lifetime context.
	Context() context.Context
	// Recv returns the next host-to-plugin frame or a terminal read error.
	Recv() (ClientFrame, error)
	// Send sends one plugin-to-host frame.
	Send(frame ServerFrame) error
}
