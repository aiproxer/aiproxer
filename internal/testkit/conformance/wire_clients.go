package conformance

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	refanthropic "github.com/aiproxer/aiproxer/internal/refclient/anthropicmessages"
	refgemini "github.com/aiproxer/aiproxer/internal/refclient/gemini"
	refopenaichat "github.com/aiproxer/aiproxer/internal/refclient/openaichat"
	refopenairesponses "github.com/aiproxer/aiproxer/internal/refclient/openairesponses"
	"github.com/aiproxer/aiproxer/internal/testkit"
	"google.golang.org/genai"
)

// GeminiConformanceBaseURL is the genai client base URL for conformance tests against the
// bundled Gemini frontend (mounted under /v1beta/ and /v1beta1/).
func GeminiConformanceBaseURL(proxyOrigin string) string {
	return strings.TrimRight(proxyOrigin, "/") + "/v1beta"
}

func wireModelForFrontend(frontendID string) string {
	switch frontendID {
	case "anthropic":
		return "claude-3-5-haiku-20241022"
	case "gemini":
		return "gemini-2.0-flash"
	default:
		return "gpt-4o-mini"
	}
}

// openResponsesWireRoundTrip drives the OpenResponses frontend over the harness
// raw wire client (JSON or SSE) and returns the assembled assistant text.
func openResponsesWireRoundTrip(tb testing.TB, proxyOrigin string, httpClient *http.Client, transport ClientTransport) (RoundTripResult, error) {
	tb.Helper()
	client := newOpenResponsesHTTPClient(proxyOrigin, httpClient, transport, wireModelForFrontend("openresponses"))
	return client.RoundTrip(context.Background(), "ping")
}

func nonStreamAssistantText(tb testing.TB, frontendID, proxyOrigin string, httpClient *http.Client) string {
	tb.Helper()
	ctx := context.Background()
	switch frontendID {
	case "openai-responses":
		cli := refopenairesponses.New(refopenairesponses.Config{
			BaseURL:    strings.TrimRight(proxyOrigin, "/") + "/v1",
			APIKey:     "sk-test",
			HTTPClient: httpClient,
		})
		res, err := cli.CreateResponse(ctx, responses.ResponseNewParams{
			Model: shared.ResponsesModel(wireModelForFrontend(frontendID)),
			Input: responses.ResponseNewParamsInputUnion{
				OfInputItemList: []responses.ResponseInputItemUnionParam{
					responses.ResponseInputItemParamOfMessage("ping", responses.EasyInputMessageRoleUser),
				},
			},
		})
		if err != nil {
			tb.Fatalf("responses non-stream: %v", err)
		}
		return responsesOutputText(res)
	case "openai-legacy":
		cli := refopenaichat.New(refopenaichat.Config{
			BaseURL:    strings.TrimRight(proxyOrigin, "/") + "/v1",
			APIKey:     "sk-test",
			HTTPClient: httpClient,
		})
		res, err := cli.CreateChatCompletion(ctx, openai.ChatCompletionNewParams{
			Model: "gpt-4o-mini",
			Messages: []openai.ChatCompletionMessageParamUnion{
				openai.UserMessage("ping"),
			},
		})
		if err != nil {
			tb.Fatalf("chat non-stream: %v", err)
		}
		if len(res.Choices) == 0 {
			tb.Fatal("no choices")
		}
		return res.Choices[0].Message.Content
	case "anthropic":
		cli := refanthropic.New(refanthropic.Config{
			BaseURL:    proxyOrigin,
			APIKey:     testkit.SyntheticAnthropicAPIKey,
			HTTPClient: httpClient,
		})
		msg, err := cli.CreateMessage(ctx, anthropic.MessageNewParams{
			Model:     anthropic.Model(wireModelForFrontend(frontendID)),
			MaxTokens: 64,
			Messages: []anthropic.MessageParam{
				anthropic.NewUserMessage(anthropic.NewTextBlock("ping")),
			},
		})
		if err != nil {
			tb.Fatalf("anthropic non-stream: %v", err)
		}
		if len(msg.Content) == 0 {
			tb.Fatal("no content")
		}
		return msg.Content[0].AsText().Text
	case "gemini":
		cli, err := refgemini.New(ctx, refgemini.Config{
			BaseURL:    GeminiConformanceBaseURL(proxyOrigin),
			APIKey:     "fake-key",
			HTTPClient: httpClient,
		})
		if err != nil {
			tb.Fatalf("gemini client: %v", err)
		}
		out, err := cli.GenerateContent(ctx, wireModelForFrontend(frontendID), []*genai.Content{
			genai.NewContentFromText("ping", genai.RoleUser),
		}, nil)
		if err != nil {
			tb.Fatalf("gemini non-stream: %v", err)
		}
		if len(out.Candidates) == 0 || out.Candidates[0].Content == nil || len(out.Candidates[0].Content.Parts) == 0 {
			tb.Fatalf("gemini candidates: %+v", out.Candidates)
		}
		return out.Candidates[0].Content.Parts[0].Text
	case "openresponses":
		res, err := openResponsesWireRoundTrip(tb, proxyOrigin, httpClient, TransportJSON)
		if err != nil {
			tb.Fatalf("openresponses non-stream: %v", err)
		}
		return res.Text
	default:
		tb.Fatalf("unknown frontend %q", frontendID)
		return ""
	}
}

// nonStreamAssistantTextWithAPIKey is like [nonStreamAssistantText] but uses apiKey for wire authentication
// (Bearer / x-api-key / Gemini API key), for multi_user local_api_key conformance paths.
func nonStreamAssistantTextWithAPIKey(tb testing.TB, frontendID, proxyOrigin string, httpClient *http.Client, apiKey string) string {
	tb.Helper()
	ctx := context.Background()
	switch frontendID {
	case "openai-responses":
		cli := refopenairesponses.New(refopenairesponses.Config{
			BaseURL:    strings.TrimRight(proxyOrigin, "/") + "/v1",
			APIKey:     apiKey,
			HTTPClient: httpClient,
		})
		res, err := cli.CreateResponse(ctx, responses.ResponseNewParams{
			Model: shared.ResponsesModel(wireModelForFrontend(frontendID)),
			Input: responses.ResponseNewParamsInputUnion{
				OfInputItemList: []responses.ResponseInputItemUnionParam{
					responses.ResponseInputItemParamOfMessage("ping", responses.EasyInputMessageRoleUser),
				},
			},
		})
		if err != nil {
			tb.Fatalf("responses non-stream: %v", err)
		}
		return responsesOutputText(res)
	case "openai-legacy":
		cli := refopenaichat.New(refopenaichat.Config{
			BaseURL:    strings.TrimRight(proxyOrigin, "/") + "/v1",
			APIKey:     apiKey,
			HTTPClient: httpClient,
		})
		res, err := cli.CreateChatCompletion(ctx, openai.ChatCompletionNewParams{
			Model: "gpt-4o-mini",
			Messages: []openai.ChatCompletionMessageParamUnion{
				openai.UserMessage("ping"),
			},
		})
		if err != nil {
			tb.Fatalf("chat non-stream: %v", err)
		}
		if len(res.Choices) == 0 {
			tb.Fatal("no choices")
		}
		return res.Choices[0].Message.Content
	case "anthropic":
		cli := refanthropic.New(refanthropic.Config{
			BaseURL:    proxyOrigin,
			APIKey:     apiKey,
			HTTPClient: httpClient,
		})
		msg, err := cli.CreateMessage(ctx, anthropic.MessageNewParams{
			Model:     anthropic.Model(wireModelForFrontend(frontendID)),
			MaxTokens: 64,
			Messages: []anthropic.MessageParam{
				anthropic.NewUserMessage(anthropic.NewTextBlock("ping")),
			},
		})
		if err != nil {
			tb.Fatalf("anthropic non-stream: %v", err)
		}
		if len(msg.Content) == 0 {
			tb.Fatal("no content")
		}
		return msg.Content[0].AsText().Text
	case "gemini":
		cli, err := refgemini.New(ctx, refgemini.Config{
			BaseURL:    GeminiConformanceBaseURL(proxyOrigin),
			APIKey:     apiKey,
			HTTPClient: httpClient,
		})
		if err != nil {
			tb.Fatalf("gemini client: %v", err)
		}
		out, err := cli.GenerateContent(ctx, wireModelForFrontend(frontendID), []*genai.Content{
			genai.NewContentFromText("ping", genai.RoleUser),
		}, nil)
		if err != nil {
			tb.Fatalf("gemini non-stream: %v", err)
		}
		if len(out.Candidates) == 0 || out.Candidates[0].Content == nil || len(out.Candidates[0].Content.Parts) == 0 {
			tb.Fatalf("gemini candidates: %+v", out.Candidates)
		}
		return out.Candidates[0].Content.Parts[0].Text
	default:
		tb.Fatalf("unknown frontend %q", frontendID)
		return ""
	}
}

func streamAssistantText(tb testing.TB, frontendID, proxyOrigin string, httpClient *http.Client) string {
	tb.Helper()
	ctx := context.Background()
	switch frontendID {
	case "openai-responses":
		cli := refopenairesponses.New(refopenairesponses.Config{
			BaseURL:    strings.TrimRight(proxyOrigin, "/") + "/v1",
			APIKey:     "sk-test",
			HTTPClient: httpClient,
		})
		stream := cli.CreateResponseStream(ctx, responses.ResponseNewParams{
			Model: shared.ResponsesModel(wireModelForFrontend(frontendID)),
			Input: responses.ResponseNewParamsInputUnion{
				OfInputItemList: []responses.ResponseInputItemUnionParam{
					responses.ResponseInputItemParamOfMessage("ping", responses.EasyInputMessageRoleUser),
				},
			},
		})
		var b strings.Builder
		for stream.Next() {
			cur := stream.Current()
			if cur.Type != "response.completed" {
				continue
			}
			raw, err := json.Marshal(cur.Response)
			if err != nil {
				tb.Fatal(err)
			}
			txt := responsesOutputTextFromRaw(raw)
			b.WriteString(txt)
		}
		if err := stream.Err(); err != nil {
			tb.Fatalf("responses stream: %v", err)
		}
		return b.String()
	case "openai-legacy":
		cli := refopenaichat.New(refopenaichat.Config{
			BaseURL:    strings.TrimRight(proxyOrigin, "/") + "/v1",
			APIKey:     "sk-test",
			HTTPClient: httpClient,
		})
		stream := cli.CreateChatCompletionStream(ctx, openai.ChatCompletionNewParams{
			Model: "gpt-4o-mini",
			Messages: []openai.ChatCompletionMessageParamUnion{
				openai.UserMessage("ping"),
			},
		})
		var b strings.Builder
		for stream.Next() {
			ch := stream.Current()
			if len(ch.Choices) > 0 && ch.Choices[0].Delta.Content != "" {
				b.WriteString(ch.Choices[0].Delta.Content)
			}
		}
		if err := stream.Err(); err != nil {
			tb.Fatalf("chat stream: %v", err)
		}
		return b.String()
	case "anthropic":
		cli := refanthropic.New(refanthropic.Config{
			BaseURL:    proxyOrigin,
			APIKey:     testkit.SyntheticAnthropicAPIKey,
			HTTPClient: httpClient,
		})
		stream := cli.CreateMessageStream(ctx, anthropic.MessageNewParams{
			Model:     anthropic.Model(wireModelForFrontend(frontendID)),
			MaxTokens: 64,
			Messages: []anthropic.MessageParam{
				anthropic.NewUserMessage(anthropic.NewTextBlock("ping")),
			},
		})
		var b strings.Builder
		for stream.Next() {
			ev := stream.Current()
			if ev.Type != "content_block_delta" {
				continue
			}
			cb := ev.AsContentBlockDelta()
			td := cb.Delta.AsTextDelta()
			b.WriteString(td.Text)
		}
		if err := stream.Err(); err != nil {
			tb.Fatalf("anthropic stream: %v", err)
		}
		return b.String()
	case "gemini":
		cli, err := refgemini.New(ctx, refgemini.Config{
			BaseURL:    GeminiConformanceBaseURL(proxyOrigin),
			APIKey:     "fake-key",
			HTTPClient: httpClient,
		})
		if err != nil {
			tb.Fatalf("gemini client: %v", err)
		}
		var b strings.Builder
		for res, serr := range cli.GenerateContentStream(ctx, wireModelForFrontend(frontendID),
			[]*genai.Content{genai.NewContentFromText("ping", genai.RoleUser)}, nil) {
			if serr != nil {
				tb.Fatalf("gemini stream: %v", serr)
			}
			for _, c := range res.Candidates {
				if c.Content == nil {
					continue
				}
				for _, p := range c.Content.Parts {
					b.WriteString(p.Text)
				}
			}
		}
		return b.String()
	case "openresponses":
		res, err := openResponsesWireRoundTrip(tb, proxyOrigin, httpClient, TransportSSE)
		if err != nil {
			tb.Fatalf("openresponses stream: %v", err)
		}
		return res.Text
	default:
		tb.Fatalf("unknown frontend %q", frontendID)
		return ""
	}
}

func responsesOutputText(res *responses.Response) string {
	if res == nil || len(res.Output) == 0 {
		return ""
	}
	for _, o := range res.Output {
		for _, c := range o.Content {
			if strings.TrimSpace(c.Text) != "" {
				return c.Text
			}
		}
	}
	return ""
}

func responsesOutputTextFromRaw(raw []byte) string {
	var w struct {
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if json.Unmarshal(raw, &w) != nil {
		return ""
	}
	for _, o := range w.Output {
		if o.Type != "message" {
			continue
		}
		for _, c := range o.Content {
			if c.Type == "output_text" {
				return c.Text
			}
		}
	}
	return ""
}
