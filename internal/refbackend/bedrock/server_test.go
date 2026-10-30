package bedrock_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	refbackend "github.com/aiproxer/aiproxer/internal/refbackend/bedrock"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

func testClient(t *testing.T, srv *httptest.Server) *bedrockruntime.Client {
	t.Helper()
	cfg, err := config.LoadDefaultConfig(
		context.Background(),
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("AKID", "SECRET", "")),
	)
	if err != nil {
		t.Fatal(err)
	}
	return bedrockruntime.NewFromConfig(cfg, func(o *bedrockruntime.Options) {
		o.BaseEndpoint = aws.String(srv.URL)
		o.EndpointOptions.DisableHTTPS = true
	})
}

func TestHandler_Converse_smoke(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	cli := testClient(t, srv)
	out, err := cli.Converse(context.Background(), &bedrockruntime.ConverseInput{
		ModelId: aws.String("anthropic.claude-3-haiku-20240307-v1:0"),
		Messages: []types.Message{{
			Role: types.ConversationRoleUser,
			Content: []types.ContentBlock{
				&types.ContentBlockMemberText{Value: "ping"},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	msg, ok := out.Output.(*types.ConverseOutputMemberMessage)
	if !ok {
		t.Fatalf("output type %T", out.Output)
	}
	if msg.Value.Role != types.ConversationRoleAssistant {
		t.Fatalf("role: %s", msg.Value.Role)
	}
	if len(msg.Value.Content) != 1 {
		t.Fatalf("content len: %d", len(msg.Value.Content))
	}
	tb, ok := msg.Value.Content[0].(*types.ContentBlockMemberText)
	if !ok || tb.Value != "ok" {
		t.Fatalf("text block: %#v", msg.Value.Content[0])
	}
}

func TestHandler_ConverseStream_smoke(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	cli := testClient(t, srv)
	streamOut, err := cli.ConverseStream(context.Background(), &bedrockruntime.ConverseStreamInput{
		ModelId: aws.String("anthropic.claude-3-haiku-20240307-v1:0"),
		Messages: []types.Message{{
			Role: types.ConversationRoleUser,
			Content: []types.ContentBlock{
				&types.ContentBlockMemberText{Value: "hi"},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	stream := streamOut.GetStream()
	defer func() { _ = stream.Close() }()

	var sawText bool
	for ev := range stream.Events() {
		switch v := ev.(type) {
		case *types.ConverseStreamOutputMemberContentBlockDelta:
			if tv, ok := v.Value.Delta.(*types.ContentBlockDeltaMemberText); ok && tv.Value == "stream-ok" {
				sawText = true
			}
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if !sawText {
		t.Fatal("expected contentBlockDelta text stream-ok")
	}
}

func TestHandler_Converse_requiresAuthorization(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{}))
	t.Cleanup(srv.Close)

	cfg, err := config.LoadDefaultConfig(
		context.Background(),
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("x", "y", "")),
	)
	if err != nil {
		t.Fatal(err)
	}
	cli := bedrockruntime.NewFromConfig(cfg, func(o *bedrockruntime.Options) {
		o.BaseEndpoint = aws.String(srv.URL)
		o.EndpointOptions.DisableHTTPS = true
		o.HTTPClient = &http.Client{
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				req.Header.Del("Authorization")
				return http.DefaultTransport.RoundTrip(req)
			}),
		}
	})
	_, err = cli.Converse(context.Background(), &bedrockruntime.ConverseInput{
		ModelId: aws.String("m"),
		Messages: []types.Message{{
			Role:    types.ConversationRoleUser,
			Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: "x"}},
		}},
	})
	if err == nil {
		t.Fatal("expected error without Authorization header")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestHandler_Converse_multimodalRequest_observedInBody(t *testing.T) {
	t.Parallel()
	const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	pdfBytes := []byte("%PDF-1.4\n1 0 obj<<>>endobj\ntrailer<<>>\n%%EOF")

	var sawImage, sawDoc bool
	srv := httptest.NewServer(refbackend.NewHandler(refbackend.Config{
		OnRequestBody: func(body []byte) {
			s := string(body)
			if strings.Contains(s, `"image"`) && strings.Contains(s, base64.StdEncoding.EncodeToString(mustDecodeB64(tinyPNG))) {
				sawImage = true
			}
			if strings.Contains(s, `"document"`) && strings.Contains(s, base64.StdEncoding.EncodeToString(pdfBytes)) {
				sawDoc = true
			}
		},
	}))
	t.Cleanup(srv.Close)

	cli := testClient(t, srv)
	_, err := cli.Converse(context.Background(), &bedrockruntime.ConverseInput{
		ModelId: aws.String("anthropic.claude-3-haiku-20240307-v1:0"),
		Messages: []types.Message{{
			Role: types.ConversationRoleUser,
			Content: []types.ContentBlock{
				&types.ContentBlockMemberImage{Value: types.ImageBlock{
					Format: types.ImageFormatPng,
					Source: &types.ImageSourceMemberBytes{Value: mustDecodeB64(tinyPNG)},
				}},
				&types.ContentBlockMemberDocument{Value: types.DocumentBlock{
					Name:   aws.String("minimal.pdf"),
					Format: types.DocumentFormatPdf,
					Source: &types.DocumentSourceMemberBytes{Value: pdfBytes},
				}},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawImage || !sawDoc {
		t.Fatalf("multimodal request body: image=%v doc=%v", sawImage, sawDoc)
	}
}

func mustDecodeB64(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}
