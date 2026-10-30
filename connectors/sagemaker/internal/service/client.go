package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/aiproxer/aiproxer/pkg/aipapi"
	"github.com/aiproxer/aiproxer/pkg/aipsdk/backendplugin"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sagemakerruntime"
)

type HFTextGenRequest struct {
	Inputs     string           `json:"inputs"`
	Parameters *HFTextGenParams `json:"parameters,omitempty"`
}

type HFTextGenParams struct {
	MaxNewTokens *int `json:"max_new_tokens,omitempty"`
}

// maxSageMakerResponseBytes bounds the unary InvokeEndpoint body accepted for
// the hf-text-generation contract. Responses larger than this fail closed
// instead of growing an unbounded buffer.
const maxSageMakerResponseBytes = 4 << 20

type Client struct {
	Config  Config
	Runtime RuntimeClient
}

func parseHFTextGenResponse(raw []byte) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return "", fmt.Errorf("sagemaker: empty response body")
	}
	if raw[0] == '[' {
		var arr []struct {
			GeneratedText string `json:"generated_text"`
		}
		if err := json.Unmarshal(raw, &arr); err != nil {
			return "", fmt.Errorf("sagemaker: unmarshal array response: %w", err)
		}
		if len(arr) == 0 {
			return "", fmt.Errorf("sagemaker: empty response array")
		}
		return arr[0].GeneratedText, nil
	}

	var obj struct {
		GeneratedText string `json:"generated_text"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", fmt.Errorf("sagemaker: unmarshal object response: %w", err)
	}
	return obj.GeneratedText, nil
}

func (c *Client) Open(ctx context.Context, call aipapi.Call, model string) (aipapi.ManagedEventStream, error) {
	if ctx == nil {
		return nil, aipapi.ErrNilContext
	}
	if len(call.Tools) > 0 {
		return nil, fmt.Errorf("sagemaker: tools are unsupported in this connector")
	}

	var inputsBuilder strings.Builder
	for _, m := range call.Messages {
		if m.Role != aipapi.RoleUser {
			return nil, fmt.Errorf("sagemaker: role %q is unsupported in hf-text-generation contract", m.Role)
		}
		for _, p := range m.Parts {
			if p.Kind != aipapi.PartText {
				return nil, fmt.Errorf("sagemaker: part kind %q is unsupported in hf-text-generation contract", p.Kind)
			}
			inputsBuilder.WriteString(p.Text)
		}
	}
	inputs := inputsBuilder.String()
	if strings.TrimSpace(inputs) == "" {
		return nil, fmt.Errorf("sagemaker: empty inputs")
	}

	var params *HFTextGenParams
	if call.Options.MaxOutputTokens != nil {
		params = &HFTextGenParams{
			MaxNewTokens: call.Options.MaxOutputTokens,
		}
	}

	reqBody := HFTextGenRequest{
		Inputs:     inputs,
		Parameters: params,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("sagemaker: marshal request: %w", err)
	}

	// Provider streaming is intentionally not used for the hf-text-generation
	// contract: the contract defines a single generated_text JSON document with
	// no incremental token framing, so the connector collects the unary
	// InvokeEndpoint response. This avoids buffering an unbounded event stream
	// before the first delta.
	input := &sagemakerruntime.InvokeEndpointInput{
		EndpointName: aws.String(model),
		ContentType:  aws.String("application/json"),
		Accept:       aws.String("application/json"),
		Body:         bodyBytes,
	}
	if c.Config.TargetModel != "" {
		input.TargetModel = aws.String(c.Config.TargetModel)
	}
	if c.Config.InferenceComponentName != "" {
		input.InferenceComponentName = aws.String(c.Config.InferenceComponentName)
	}

	resp, err := c.Runtime.InvokeEndpoint(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("sagemaker: invoke endpoint: %w", err)
	}
	if len(resp.Body) > maxSageMakerResponseBytes {
		return nil, fmt.Errorf("sagemaker: response body %d bytes exceeds limit %d bytes", len(resp.Body), maxSageMakerResponseBytes)
	}

	genText, err := parseHFTextGenResponse(resp.Body)
	if err != nil {
		return nil, err
	}

	events := []aipapi.Event{
		{Kind: aipapi.EventResponseStarted},
		{Kind: aipapi.EventMessageStarted},
		{Kind: aipapi.EventTextDelta, Delta: genText},
		{Kind: aipapi.EventResponseFinished},
	}
	return aipapi.CloseOnlyManagedStream{Stream: &sliceStream{events: events}}, nil
}

func (c *Client) ListModels(ctx context.Context, limit uint32) (backendplugin.ListModelsResponse, error) {
	if ctx == nil {
		return backendplugin.ListModelsResponse{}, aipapi.ErrNilContext
	}
	// Expose only the configured endpoint under its deterministic
	// hf-text-generation contract. Arbitrary InService endpoints may front
	// arbitrary containers, so enumerating them would advertise models this
	// connector refuses to execute (Execute fails closed unless the name
	// matches endpoint_name). There is no per-endpoint contract registry in
	// config, so fail closed to exactly one model.
	name := strings.TrimSpace(c.Config.EndpointName)
	if name == "" {
		return backendplugin.ListModelsResponse{}, fmt.Errorf("sagemaker: endpoint_name is not configured")
	}
	_ = limit
	return backendplugin.ListModelsResponse{
		Models: []backendplugin.ModelDescriptor{{
			CanonicalModelID: FactoryKind + "/" + name,
			NativeModelID:    name,
			FactoryKind:      FactoryKind,
			// Provider streaming is served via unary InvokeEndpoint collect:
			// canonical events are emitted over a managed stream.
			Capabilities: backendplugin.CapabilitySummary{Streaming: true},
		}},
		InventorySource: FactoryKind,
		FetchedUnixMS:   time.Now().UnixMilli(),
	}, nil
}

type sliceStream struct {
	mu     sync.Mutex
	events []aipapi.Event
	idx    int
	closed bool
}

func (s *sliceStream) Recv(context.Context) (aipapi.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return aipapi.Event{}, fmt.Errorf("sagemaker: stream closed")
	}
	if s.idx >= len(s.events) {
		return aipapi.Event{}, io.EOF
	}
	ev := s.events[s.idx]
	s.idx++
	return ev, nil
}

func (s *sliceStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}
