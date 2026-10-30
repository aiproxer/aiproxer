package adapter_test

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"

	backendpluginv1 "github.com/aiproxer/aiproxer/api/backendplugin/v1"
	app "github.com/aiproxer/aiproxer/internal/core/tokenaccounting/app"
	"github.com/aiproxer/aiproxer/internal/infra/backendplugins/adapter"
	testkit "github.com/aiproxer/aiproxer/internal/testkit/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin/host"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestBufconn_RealServerExecuteWithInvocation(t *testing.T) {
	t.Parallel()
	lis := bufconn.Listen(1 << 20)
	offer := backendplugin.ProtocolOffer{
		Major: 1, Minor: 0, DisableTransportRetries: true,
		Features: []backendplugin.Feature{
			{Name: "count_tokens"}, {Name: "finalize_billing"},
		},
	}
	gs := grpc.NewServer()
	backendpluginv1.RegisterBackendPluginServer(gs, backendplugin.NewGRPCServer(offer, &testkit.FakeService{Mode: testkit.ModeValid}))
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(func() {
		gs.Stop()
		_ = lis.Close()
	})

	dialer := func(context.Context, string) (net.Conn, error) { return lis.Dial() }
	conn, err := grpc.NewClient("passthrough:///buf", grpc.WithContextDialer(dialer), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := backendpluginv1.NewBackendPluginClient(conn)

	neg, err := client.Negotiate(context.Background(), &backendpluginv1.NegotiateRequest{
		HostMajor: 1, HostMinor: 0, DisableTransportRetries: true,
		HostFeatures: []*backendpluginv1.Feature{{Name: "count_tokens"}, {Name: "finalize_billing"}},
	})
	if err != nil || !neg.GetCompatible() {
		t.Fatalf("negotiate: %+v %v", neg, err)
	}
	_, err = client.Configure(context.Background(), &backendpluginv1.ConfigureRequest{
		InstanceId: "buf1", FactoryKind: "fake", ConfigYaml: []byte("k: v\n"),
		NegotiationToken: neg.GetNegotiationToken(),
		RuntimePolicy:    &backendpluginv1.RuntimePolicy{DisableTransportRetries: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	sess := host.NewSessionForTesting(client, conn, "buf1", backendplugin.Negotiation{})
	profile, err := sess.Resolve(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	br := adapter.Build(sess, profile, adapter.Options{InstanceID: "buf1", RoutePrefixes: []string{"fake"}})
	stream, err := br.Backend.Open(context.Background(), testCall(), testCand())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stream.Close() })

	var sawText bool
	for {
		ev, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Kind == aipapi.EventTextDelta {
			sawText = true
		}
	}
	if !sawText {
		t.Fatal("expected real GRPCServer execute path to deliver text")
	}
}

func TestDialConfiguredSession_negotiatesOrderedItemsAndExecutes(t *testing.T) {
	t.Parallel()

	fake := &testkit.FakeService{Mode: testkit.ModeValid}
	lis := bufconn.Listen(1 << 20)
	offer := backendplugin.ProtocolOffer{
		Major: 1, Minor: backendplugin.ProtocolMinorOrderedItems, DisableTransportRetries: true,
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureOrderedItems},
			{Name: backendplugin.FeatureExactReasoningParts},
		},
	}
	gs := grpc.NewServer()
	backendpluginv1.RegisterBackendPluginServer(gs, backendplugin.NewGRPCServer(offer, fake))
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(func() {
		gs.Stop()
		_ = lis.Close()
	})

	conn, err := lis.Dial()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	sess, profile, err := adapter.DialConfiguredSession(
		context.Background(), conn, "dial-ord", "fake", []byte("k: v\n"),
		backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 8},
	)
	if err != nil {
		t.Fatalf("DialConfiguredSession: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })
	if sess.Negotiation().NegotiatedMinor < backendplugin.ProtocolMinorOrderedItems {
		t.Fatalf("negotiation=%#v", sess.Negotiation())
	}
	if !profile.Capabilities.OrderedItems {
		t.Fatalf("profile=%#v", profile)
	}

	br := adapter.Build(sess, profile, adapter.Options{
		InstanceID:  "dial-ord",
		Negotiation: sess.Negotiation(),
	})
	call := aipapi.Call{
		ID:         "req-dial",
		Session:    aipapi.SessionRef{ALegID: "aleg-dial"},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenResponsesCreate},
		Items: []aipapi.Item{{
			Kind: aipapi.ItemKindMessage, ID: "msg-1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hi"}},
		}},
	}
	stream, err := br.Backend.Open(context.Background(), call, testCand())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	for {
		_, err := stream.Recv(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if fake.ExecuteCount.Load() != 1 {
		t.Fatalf("ExecuteCount=%d want 1", fake.ExecuteCount.Load())
	}
	if fake.LastStartCall == nil || len(fake.LastStartCall.Items) != 1 {
		t.Fatalf("reconstructed call=%#v", fake.LastStartCall)
	}
}

func TestDialConfiguredSession_PreservesOptionalAccountingThroughInternalAlias(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeValid}
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	backendpluginv1.RegisterBackendPluginServer(gs, backendplugin.NewGRPCServer(backendplugin.ProtocolOffer{
		Major: 1, Minor: backendplugin.ProtocolMinorProxyOwnedSessionID, DisableTransportRetries: true,
		Features: []backendplugin.Feature{{Name: backendplugin.FeatureProxyOwnedSessionID}},
	}, fake))
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(func() { gs.Stop(); _ = lis.Close() })

	conn, err := lis.Dial()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	sess, profile, err := adapter.DialConfiguredSession(context.Background(), conn, "accounting-alias", "fake", nil, backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })

	if _, ok := any(sess).(adapter.OptionalTokenCounter); !ok {
		t.Fatal("internal GRPCSession alias lost OptionalTokenCounter")
	}
	if _, ok := any(sess).(adapter.OptionalBillingFinalizer); !ok {
		t.Fatal("internal GRPCSession alias lost OptionalBillingFinalizer")
	}
	count, err := sess.CountTokens(context.Background(), backendplugin.CountTokensRequest{
		InstanceID: "accounting-alias", ModelID: "fake-model", Invocation: backendInvocation(),
	})
	if err != nil || count.InputTokens == nil {
		t.Fatalf("count=%+v err=%v", count, err)
	}
	final, err := sess.FinalizeBilling(context.Background(), backendplugin.FinalizeBillingRequest{
		InstanceID: "accounting-alias", ALegID: "a", BLegID: "b", ModelID: "fake-model", IdempotencyKey: "idem",
	})
	if err != nil || final.Usage.TotalTokens == nil {
		t.Fatalf("final=%+v err=%v", final, err)
	}
	br := adapter.Build(sess, profile, adapter.Options{InstanceID: "accounting-alias"})
	if br.Backend.ProviderCounter == nil || br.Backend.FinalizeBilling == nil {
		t.Fatal("internal adapter did not expose optional accounting seams")
	}
}

func backendInvocation() backendplugin.Invocation {
	text := "hello"
	return backendplugin.Invocation{
		RequestID: "request", AttemptID: "attempt", ALegID: "a", BLegID: "b", CanonicalModelID: "fake-model",
		Messages: []backendplugin.Message{{Role: backendplugin.RoleUser, Parts: []backendplugin.Part{{Kind: backendplugin.PartKindText, Text: &text}}}},
	}
}

// TestDialConfiguredSession_ProviderCountReachesConnectorWithPayload exercises
// the count bridge through a real gRPC session, where
// CountTokensRequestToProto rejects an invalid Invocation. It proves the
// caller's canonical payload survives the wire and that an empty call fails
// instead of returning a fabricated zero.
func TestDialConfiguredSession_ProviderCountReachesConnectorWithPayload(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeValid}
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	backendpluginv1.RegisterBackendPluginServer(gs, backendplugin.NewGRPCServer(backendplugin.ProtocolOffer{
		Major: 1, Minor: backendplugin.ProtocolMinorProxyOwnedSessionID, DisableTransportRetries: true,
		Features: []backendplugin.Feature{{Name: backendplugin.FeatureProxyOwnedSessionID}},
	}, fake))
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(func() { gs.Stop(); _ = lis.Close() })

	conn, err := lis.Dial()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	sess, profile, err := adapter.DialConfiguredSession(context.Background(), conn, "count-wire", "fake", nil, backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })

	br := adapter.Build(sess, profile, adapter.Options{InstanceID: "count-wire", Negotiation: sess.Negotiation()})
	t.Cleanup(func() { _ = br.Cleanup() })
	counter := br.Backend.ProviderCounter
	if counter == nil {
		t.Fatal("expected provider counter")
	}

	call := countPayloadCall()
	res, err := counter.CountCall(context.Background(), app.CountCallInput{
		Backend: "fake", Model: "fake-model", CallID: call.ID, Call: call,
	})
	if err != nil {
		t.Fatalf("CountCall over the wire: %v", err)
	}
	if res.InputTokens != 3 {
		t.Fatalf("InputTokens=%d want 3: the wire must carry the caller's messages", res.InputTokens)
	}

	empty, err := counter.CountCall(context.Background(), app.CountCallInput{
		Backend: "fake", Model: "fake-model", CallID: "req-empty",
		Call: aipapi.Call{ID: "req-empty", Session: aipapi.SessionRef{ALegID: "aleg-1"}},
	})
	if err == nil {
		t.Fatalf("empty call must not yield provider evidence: %+v", empty)
	}
	if empty.InputTokens != 0 || empty.TotalTokensPresent {
		t.Fatalf("failed count must return zero evidence: %+v", empty)
	}
}

func TestDialConfiguredSession_rejectsItemAuthorityOnOldMinorRealGRPC(t *testing.T) {
	t.Parallel()
	runDialConfiguredOrderedItemRejection(t, backendplugin.ProtocolOffer{
		Major: 1, Minor: 0, DisableTransportRetries: true,
		Features: []backendplugin.Feature{{Name: backendplugin.FeatureExactReasoningParts}},
	}, "old-minor")
}

func TestDialConfiguredSession_rejectsItemAuthorityWhenMinor2MissingOrderedItemsFeature(t *testing.T) {
	t.Parallel()
	runDialConfiguredOrderedItemRejection(t, backendplugin.ProtocolOffer{
		Major: 1, Minor: backendplugin.ProtocolMinorOrderedItems, DisableTransportRetries: true,
		Features: []backendplugin.Feature{{Name: backendplugin.FeatureExactReasoningParts}},
	}, "minor2-no-feature")
}

func runDialConfiguredOrderedItemRejection(t *testing.T, offer backendplugin.ProtocolOffer, instanceID string) {
	t.Helper()
	fake := &testkit.FakeService{Mode: testkit.ModeValid}
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	backendpluginv1.RegisterBackendPluginServer(gs, backendplugin.NewGRPCServer(offer, fake))
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(func() {
		gs.Stop()
		_ = lis.Close()
	})

	conn, err := lis.Dial()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	sess, profile, err := adapter.DialConfiguredSession(
		context.Background(), conn, instanceID, "fake", []byte("k: v\n"),
		backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 8},
	)
	if err != nil {
		t.Fatalf("DialConfiguredSession: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })

	br := adapter.Build(sess, profile, adapter.Options{
		InstanceID:  instanceID,
		Negotiation: sess.Negotiation(),
	})
	call := aipapi.Call{
		ID:         "req-reject",
		Session:    aipapi.SessionRef{ALegID: "aleg-reject"},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenResponsesCreate},
		Items: []aipapi.Item{{
			Kind: aipapi.ItemKindMessage, ID: "msg-1", Status: aipapi.ItemStatusCompleted, Role: aipapi.RoleUser,
			Content: []aipapi.ContentPart{{Kind: aipapi.ContentPartText, Text: "hi"}},
		}},
	}
	_, err = br.Backend.Open(context.Background(), call, testCand())
	if err == nil {
		t.Fatal("expected ordered-item ABI rejection before connector Execute")
	}
	if fake.ExecuteCount.Load() != 0 {
		t.Fatalf("ExecuteCount=%d want zero upstream visibility", fake.ExecuteCount.Load())
	}
}

func TestBufconn_FullAdapterBridgeExecuteStream_TerminalFramePumpShutdown(t *testing.T) {
	t.Parallel()
	fake := &testkit.FakeService{Mode: testkit.ModeValid}
	lis := bufconn.Listen(1 << 20)
	offer := backendplugin.ProtocolOffer{
		Major: 1, Minor: backendplugin.ProtocolMinorAccountingEvidence, DisableTransportRetries: true,
		Features: []backendplugin.Feature{
			{Name: backendplugin.FeatureOrderedItems},
			{Name: backendplugin.FeatureAccountingEvidence},
		},
	}
	gs := grpc.NewServer()
	backendpluginv1.RegisterBackendPluginServer(gs, backendplugin.NewGRPCServer(offer, fake))
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(func() { gs.Stop(); _ = lis.Close() })

	conn, err := lis.Dial()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	sess, profile, err := adapter.DialConfiguredSession(
		context.Background(), conn, "bufconn-full", "fake", []byte("kind: fake\n"),
		backendplugin.SecretBundle{}, backendplugin.RuntimePolicy{DisableTransportRetries: true, MaxPendingEvents: 8},
	)
	if err != nil {
		t.Fatalf("DialConfiguredSession: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })

	br := adapter.Build(sess, profile, adapter.Options{
		InstanceID:  "bufconn-full",
		Negotiation: sess.Negotiation(),
	})
	call := aipapi.Call{
		ID:         "req-full",
		Session:    aipapi.SessionRef{ALegID: "aleg-full"},
		Invocation: aipapi.Invocation{Operation: aipapi.OperationOpenResponsesCreate},
		Messages:   []aipapi.Message{{Role: aipapi.RoleUser, Parts: []aipapi.Part{aipapi.TextPart("hello")}}},
	}
	stream, err := br.Backend.Open(context.Background(), call, testCand())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })

	var events []aipapi.Event
	for {
		ev, recvErr := stream.Recv(context.Background())
		if errors.Is(recvErr, io.EOF) {
			break
		}
		if recvErr != nil {
			t.Fatalf("Recv error: %v", recvErr)
		}
		events = append(events, ev)
	}

	if len(events) == 0 {
		t.Fatal("expected events delivered before terminal frame shutdown")
	}
	if fake.ExecuteCount.Load() != 1 {
		t.Fatalf("ExecuteCount=%d, want 1", fake.ExecuteCount.Load())
	}
}
