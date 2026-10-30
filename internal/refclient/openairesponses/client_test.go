package openairesponses_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	refbackendopenai "github.com/aiproxer/aiproxer/internal/refbackend/openairesponses"
	"github.com/aiproxer/aiproxer/internal/refclient/openairesponses"
	"github.com/aiproxer/aiproxer/internal/refclient/refclienttest"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

func TestCreateResponse_nonStreaming_smoke(t *testing.T) {
	t.Parallel()
	const minimalResponse = `{
  "id": "resp_test_1",
  "object": "response",
  "created_at": 1715620000,
  "status": "completed",
  "model": "gpt-4o-mini",
  "output": [
    {
      "type": "message",
      "id": "msg_out",
      "status": "completed",
      "role": "assistant",
      "content": [
        {"type": "output_text", "text": "ok"}
      ]
    }
  ]
}`

	var lastBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Errorf("expected Bearer Authorization, got %q", r.Header.Get("Authorization"))
		}
		b, _ := io.ReadAll(r.Body)
		lastBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(minimalResponse))
	}))
	t.Cleanup(srv.Close)

	cli := openairesponses.New(openairesponses.Config{
		BaseURL: srv.URL + "/v1",
		APIKey:  "sk-test",
	})

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
	if res.ID != "resp_test_1" {
		t.Fatalf("response id: got %q", res.ID)
	}
	if !strings.Contains(lastBody, `"model":"gpt-4o-mini"`) && !strings.Contains(lastBody, `"model": "gpt-4o-mini"`) {
		t.Fatalf("request should include model, got: %s", lastBody)
	}
}

func TestCreateResponse_multimodal_imageAndPDF_inRequest(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s := string(b)
		if !strings.Contains(s, "input_image") || !strings.Contains(s, "input_file") {
			t.Errorf("expected input_image and input_file in body, got: %s", s)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "resp_mm",
  "object": "response",
  "created_at": 1715620000,
  "status": "completed",
  "model": "gpt-4o-mini",
  "output": [{"type":"message","id":"m1","status":"completed","role":"assistant","content":[{"type":"output_text","text":"seen"}]}]
}`))
	}))
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

	cli := openairesponses.New(openairesponses.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
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
}

func TestCreateResponseStream_readsCompletedEvent(t *testing.T) {
	t.Parallel()
	respObj := map[string]any{
		"id": "resp_stream", "object": "response", "created_at": 1715620000.0,
		"status": "completed", "model": "gpt-4o-mini",
		"output": []any{
			map[string]any{
				"type": "message", "id": "m1", "status": "completed", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": "stream-ok"}},
			},
		},
	}
	evt := map[string]any{
		"type":            "response.completed",
		"sequence_number": 1.0,
		"response":        respObj,
	}
	evtBytes, err := json.Marshal(evt)
	if err != nil {
		t.Fatal(err)
	}

	var sb strings.Builder
	sb.WriteString("event: response.completed\n")
	sb.WriteString("data: ")
	sb.Write(evtBytes)
	sb.WriteString("\n\n")
	sb.WriteString("data: [DONE]\n\n")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, sb.String())
	}))
	t.Cleanup(srv.Close)

	cli := openairesponses.New(openairesponses.Config{BaseURL: srv.URL + "/v1", APIKey: "sk-test"})
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
		if cur.Type == "response.completed" && cur.Response.ID == "resp_stream" {
			saw = true
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if !saw {
		t.Fatal("expected response.completed with id resp_stream")
	}
}

func TestRefclient_disableSDKRetries_singleHTTPAttemptOn429(t *testing.T) {
	t.Parallel()
	var reqs atomic.Int32
	rb := refbackendopenai.NewHandler(refbackendopenai.Config{
		ForcedHTTPStatus: http.StatusTooManyRequests,
		ForcedRetryAfter: "1",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		rb.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	cli := openairesponses.New(openairesponses.Config{
		BaseURL:           srv.URL + "/v1",
		APIKey:            "sk",
		HTTPClient:        srv.Client(),
		DisableSDKRetries: true,
	})
	_, err := cli.CreateResponse(context.Background(), responses.ResponseNewParams{
		Model: shared.ResponsesModel("gpt-4o-mini"),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: []responses.ResponseInputItemUnionParam{
				responses.ResponseInputItemParamOfMessage("x", responses.EasyInputMessageRoleUser),
			},
		},
	})
	if err == nil {
		t.Fatal("expected error from 429 refbackend")
	}
	if n := reqs.Load(); n != 1 {
		t.Fatalf("upstream HTTP attempts: %d want 1", n)
	}
}
