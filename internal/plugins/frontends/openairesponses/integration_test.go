package openairesponses_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	front "github.com/aiproxer/aiproxer/internal/plugins/frontends/openairesponses"
	"github.com/aiproxer/aiproxer/internal/plugins/frontends/routeselect"
	refcli "github.com/aiproxer/aiproxer/internal/refclient/openairesponses"
	"github.com/aiproxer/aiproxer/internal/refclient/refclienttest"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

func TestIntegration_refclientNonStreaming(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "integration-ok", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cli := refcli.New(refcli.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	res, err := cli.CreateResponse(context.Background(), responses.ResponseNewParams{
		Model: shared.ResponsesModel("gpt-4o-mini"),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: []responses.ResponseInputItemUnionParam{
				responses.ResponseInputItemParamOfMessage("ping", responses.EasyInputMessageRoleUser),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output[0].Content[0].Text, "integration-ok") {
		t.Fatalf("output: %+v", res.Output)
	}
}

func TestIntegration_refclientStreaming(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "stream-ok", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cli := refcli.New(refcli.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	stream := cli.CreateResponseStream(context.Background(), responses.ResponseNewParams{
		Model: shared.ResponsesModel("gpt-4o-mini"),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: []responses.ResponseInputItemUnionParam{
				responses.ResponseInputItemParamOfMessage("hi", responses.EasyInputMessageRoleUser),
			},
		},
	})
	var saw bool
	for stream.Next() {
		cur := stream.Current()
		if cur.Type != "response.completed" {
			continue
		}
		raw, err := json.Marshal(cur.Response)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "stream-ok") {
			saw = true
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if !saw {
		t.Fatal("expected response.completed with stream-ok")
	}
}

func TestIntegration_refclientMultimodalCanonicalParts(t *testing.T) {
	t.Parallel()
	var capture sync.Map
	caps := aipapi.NewBackendCaps(
		aipapi.CapabilityStreaming,
		aipapi.CapabilityVision,
		aipapi.CapabilityDocuments,
	)
	ex := testkit.NewStubExecutor(t, caps, "seen", &capture)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	png := refclienttest.ReadRefclientFixture(t, "tiny.png")
	pdf := refclienttest.ReadRefclientFixture(t, "minimal.pdf")
	imgB64 := base64.StdEncoding.EncodeToString(png)
	pdfB64 := base64.StdEncoding.EncodeToString(pdf)
	dataImageURL := "data:image/png;base64," + imgB64

	img := responses.ResponseInputContentParamOfInputImage(responses.ResponseInputImageDetailAuto)
	img.OfInputImage.ImageURL = openai.String(dataImageURL)
	filePart := responses.ResponseInputContentUnionParam{
		OfInputFile: &responses.ResponseInputFileParam{
			FileData: openai.String(pdfB64),
			Filename: openai.String("minimal.pdf"),
		},
	}

	cli := refcli.New(refcli.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	_, err := cli.CreateResponse(context.Background(), responses.ResponseNewParams{
		Model: shared.ResponsesModel("gpt-4o-mini"),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: []responses.ResponseInputItemUnionParam{
				responses.ResponseInputItemParamOfInputMessage(
					responses.ResponseInputMessageContentListParam{
						responses.ResponseInputContentParamOfInputText("describe attachments"),
						img,
						filePart,
					},
					"user",
				),
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	v, ok := capture.Load("last")
	if !ok {
		t.Fatal("expected captured call")
	}
	call := testkit.MustAIPCall(t, v)
	parts := call.Messages[0].Parts
	if len(parts) < 3 {
		t.Fatalf("parts: %+v", parts)
	}
	if parts[1].Kind != aipapi.PartImageRef {
		t.Fatalf("want image part, got %+v", parts[1])
	}
	if parts[2].Kind != aipapi.PartFileRef || parts[2].FileMIME != "application/pdf" {
		t.Fatalf("want pdf file part, got %+v", parts[2])
	}
}

func TestIntegration_invalidPath_returns404(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "x", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	hc := testkit.LocalTestServerHTTPClient()
	res, err := hc.Post(srv.URL+"/v1/other", "application/json", strings.NewReader(`{"model":"gpt-4o-mini","input":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d", res.StatusCode)
	}
}

func TestIntegration_methodNotAllowed(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "x", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := testkit.IntegrationHTTPClient(nil).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status %d", res.StatusCode)
	}
}

func TestIntegration_malformedJSON_returns400(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "x", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	hc := testkit.LocalTestServerHTTPClient()
	res, err := hc.Post(srv.URL+"/v1/responses", "application/json", strings.NewReader(`{`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusBadRequest {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d body %s", res.StatusCode, string(b))
	}
}

func TestIntegration_capabilityReject_returns400(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "nope", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	png := refclienttest.ReadRefclientFixture(t, "tiny.png")
	imgB64 := base64.StdEncoding.EncodeToString(png)
	dataImageURL := "data:image/png;base64," + imgB64
	img := responses.ResponseInputContentParamOfInputImage(responses.ResponseInputImageDetailAuto)
	img.OfInputImage.ImageURL = openai.String(dataImageURL)

	cli := refcli.New(refcli.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	_, err := cli.CreateResponse(context.Background(), responses.ResponseNewParams{
		Model: shared.ResponsesModel("gpt-4o-mini"),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: []responses.ResponseInputItemUnionParam{
				responses.ResponseInputItemParamOfInputMessage(
					responses.ResponseInputMessageContentListParam{
						responses.ResponseInputContentParamOfInputText("x"),
						img,
					},
					"user",
				),
			},
		},
	})
	if err == nil {
		t.Fatal("expected error from capability reject")
	}
}

func TestIntegration_toolStubRoundTrip_streaming(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools), "tail", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cli := refcli.New(refcli.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
	stream := cli.CreateResponseStream(context.Background(), responses.ResponseNewParams{
		Model: shared.ResponsesModel("gpt-4o-mini"),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: []responses.ResponseInputItemUnionParam{
				responses.ResponseInputItemParamOfMessage("use the tool", responses.EasyInputMessageRoleUser),
			},
		},
		Tools: []responses.ToolUnionParam{
			{OfFunction: &responses.FunctionToolParam{
				Name:        "plan_fn",
				Description: openai.String("d"),
				Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			}},
		},
	})
	var sawFunctionCallDelta, sawFunctionCallDone, sawCompleted bool
	var fcName, fcArgs string
	for stream.Next() {
		cur := stream.Current()
		switch cur.Type {
		case "response.function_call_arguments.delta":
			sawFunctionCallDelta = true
		case "response.function_call_arguments.done":
			sawFunctionCallDone = true
		case "response.completed":
			sawCompleted = true
			for _, item := range cur.Response.Output {
				if item.Type != "function_call" {
					continue
				}
				if fc := item.AsFunctionCall(); fc.ID != "" {
					fcName = fc.Name
					fcArgs = fc.Arguments
				}
			}
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if !sawFunctionCallDelta {
		t.Fatal("expected response.function_call_arguments.delta event")
	}
	if !sawFunctionCallDone {
		t.Fatal("expected response.function_call_arguments.done event")
	}
	if !sawCompleted {
		t.Fatal("expected response.completed event")
	}
	if fcName != "plan_fn" {
		t.Fatalf("function_call name: %q", fcName)
	}
	if !strings.Contains(fcArgs, `"q"`) {
		t.Fatalf("function_call arguments: %q", fcArgs)
	}
}

func TestIntegration_toolStubRoundTrip_nonStreaming(t *testing.T) {
	t.Parallel()
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming, aipapi.CapabilityTools), "tail", nil)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:gpt-4o-mini"}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	body := `{
  "model": "gpt-4o-mini",
  "stream": false,
  "tools": [{"type":"function","function":{"name":"plan_fn","description":"d","parameters":{"type":"object","properties":{}}}}],
  "input": [{"type":"message","role":"user","content":"x"}]
}`
	hc := testkit.LocalTestServerHTTPClient()
	res, err := hc.Post(srv.URL+"/v1/responses", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d body %s", res.StatusCode, string(b))
	}
	var v struct {
		Output []map[string]any `json:"output"`
	}
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	if len(v.Output) < 2 {
		t.Fatalf("output len %d", len(v.Output))
	}
	if typ, _ := v.Output[1]["type"].(string); typ != "function_call" {
		t.Fatalf("output[1] type %v", v.Output[1])
	}
	if v.Output[1]["name"] != "plan_fn" {
		t.Fatalf("name: %v", v.Output[1]["name"])
	}
	args, _ := v.Output[1]["arguments"].(string)
	if !strings.Contains(args, `"q"`) {
		t.Fatalf("arguments %q", args)
	}
}

func TestIntegration_routeHeaderOverridesDefault(t *testing.T) {
	t.Parallel()
	var capture sync.Map
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "ok", &capture)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:default-route", RoutePrefixes: routeselect.NewPrefixSet([]string{"stub"})}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(`{"model":"gpt-4o-mini","input":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(front.HeaderRouteSelector, "stub:route-from-header")
	res, err := testkit.IntegrationHTTPClient(nil).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d body %s", res.StatusCode, string(b))
	}
	v, ok := capture.Load("last")
	if !ok {
		t.Fatal("expected captured call")
	}
	call := testkit.MustAIPCall(t, v)
	if call.Route.Selector != "stub:route-from-header" {
		t.Fatalf("route selector %q", call.Route.Selector)
	}
}

func TestIntegration_modelRouteSelectorUsedWhenHeaderAbsent(t *testing.T) {
	t.Parallel()
	var capture sync.Map
	ex := testkit.NewStubExecutor(t, aipapi.NewBackendCaps(aipapi.CapabilityStreaming), "ok", &capture)
	h := &front.Handler{Exec: ex, DefaultRouteSelector: "stub:default-route", RoutePrefixes: routeselect.NewPrefixSet([]string{"stub"})}
	mux := http.NewServeMux()
	mux.Handle("/v1/responses", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/v1/responses", strings.NewReader(`{"model":"stub:model-from-body","input":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := testkit.IntegrationHTTPClient(nil).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		t.Fatalf("status %d body %s", res.StatusCode, string(b))
	}
	v, ok := capture.Load("last")
	if !ok {
		t.Fatal("expected captured call")
	}
	call := testkit.MustAIPCall(t, v)
	if call.Route.Selector != "stub:model-from-body" {
		t.Fatalf("route selector %q", call.Route.Selector)
	}
}
