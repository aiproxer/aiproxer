package codex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

// nativeLiveRequestEvidence is deliberately limited to safe shape metadata.
// The recorder never stores request bodies, headers, or opaque item contents.
type nativeLiveRequestEvidence struct {
	Path                   string
	Model                  string
	TopLevelFields         []string
	InputTypeCounts        map[string]int
	HasReasoning           bool
	ReasoningEffortPresent bool
	IncludeEncrypted       bool
	ToolCount              int
	PreviousResponseID     bool
	MetadataKeys           []string
	Compaction             bool
	TriggerCount           int
	TriggerSemantics       string
	HasCheckpoint          bool
	HasSummary             bool
	TailPreserved          bool
	CompactionTailExcluded bool
	MarkerLeaked           bool
	StatusCode             int
	Phase                  string
	Category               string
	ProviderCode           string
	ProviderType           string
	ProviderParam          string
	ResponseObject         string
	ResponseStatus         string
	ResponseTopLevelFields []string
	ResponseOutputShape    string
	ResponseOutputTypes    map[string]int
	ResponseUsagePresent   bool
	ResponseCompactField   bool
}

const nativeLiveResponseCaptureLimit = 8 << 20

var nativeLiveProviderCodes = map[string]struct{}{
	"account_deactivated": {}, "context_length_exceeded": {}, "insufficient_quota": {},
	"invalid_api_key": {}, "invalid_request_error": {}, "model_not_found": {},
	"rate_limit_exceeded": {}, "server_error": {}, "token_expired": {},
	"unsupported_model": {}, "unsupported_parameter": {}, "unsupported_value": {},
}

var nativeLiveProviderTypes = map[string]struct{}{
	"api_error": {}, "authentication_error": {}, "error": {}, "invalid_request_error": {},
	"not_found_error": {}, "permission_error": {}, "rate_limit_error": {}, "server_error": {},
}

type nativeLiveHTTPRecorder struct {
	expectedTail         string
	expectedNormalTails  []string
	expectedCompactTails []string
	normalRequests       int
	compactRequests      int
	base                 http.RoundTripper
	mu                   sync.Mutex
	records              []nativeLiveRequestEvidence
}

func newNativeLiveHTTPRecorder(expectedTail string, base http.RoundTripper) *nativeLiveHTTPRecorder {
	if base == nil {
		base = http.DefaultTransport
	}
	return &nativeLiveHTTPRecorder{expectedTail: expectedTail, base: base}
}

// SetExpectedPhaseTails lets the live proof distinguish the current turn from
// older retained turns that may legitimately remain in a later compaction.
func (r *nativeLiveHTTPRecorder) SetExpectedPhaseTails(normal, compact []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expectedNormalTails = append([]string(nil), normal...)
	r.expectedCompactTails = append([]string(nil), compact...)
	r.normalRequests = 0
	r.compactRequests = 0
}

func (r *nativeLiveHTTPRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	var err error
	if req.Body != nil {
		body, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	isCompaction := req != nil && req.URL != nil && strings.HasSuffix(strings.TrimRight(req.URL.Path, "/"), "/responses/compact")
	isAuthRefresh := req != nil && req.URL != nil && strings.Contains(req.URL.Path, "/oauth/token")
	expectedTail := r.expectedTail
	r.mu.Lock()
	if isAuthRefresh {
		// Auth refresh traffic is not part of the phase-aware proof sequence.
	} else if isCompaction {
		if r.compactRequests < len(r.expectedCompactTails) {
			expectedTail = r.expectedCompactTails[r.compactRequests]
		}
		r.compactRequests++
	} else {
		if r.normalRequests < len(r.expectedNormalTails) {
			expectedTail = r.expectedNormalTails[r.normalRequests]
		}
		r.normalRequests++
	}
	r.mu.Unlock()
	evidence := nativeLiveRequestShape(body, expectedTail)
	if req != nil && req.URL != nil {
		evidence.Path = req.URL.Path
		if isCompaction {
			evidence.Compaction = true
			evidence.TriggerSemantics = "dedicated_endpoint"
			evidence.CompactionTailExcluded = expectedTail != "" && !evidence.TailPreserved
		}
	}
	evidence.Phase = nativeLiveRequestPhase(req, evidence)
	resp, err := r.base.RoundTrip(req)
	if err != nil {
		evidence.StatusCode = 0
		evidence.Category = nativeLiveStatusCategory(0)
	} else if resp != nil {
		evidence.StatusCode = resp.StatusCode
		evidence.Category = nativeLiveStatusCategory(resp.StatusCode)
	}
	r.mu.Lock()
	recordIndex := len(r.records)
	r.records = append(r.records, evidence)
	r.mu.Unlock()
	if resp != nil && resp.Body != nil && (resp.StatusCode >= 400 || evidence.Compaction) {
		resp.Body = &nativeLiveResponseBody{body: resp.Body, onComplete: func(body []byte) {
			code, typ, param := nativeLiveProviderErrorMetadata(body)
			responseShape := nativeLiveCompactionResponseShape(body)
			r.mu.Lock()
			if recordIndex < len(r.records) {
				r.records[recordIndex].ProviderCode = code
				r.records[recordIndex].ProviderType = typ
				r.records[recordIndex].ProviderParam = param
				r.records[recordIndex].ResponseObject = responseShape.Object
				r.records[recordIndex].ResponseTopLevelFields = responseShape.TopLevelFields
				r.records[recordIndex].ResponseOutputShape = responseShape.OutputShape
				r.records[recordIndex].ResponseOutputTypes = responseShape.OutputTypes
				r.records[recordIndex].ResponseUsagePresent = responseShape.UsagePresent
				r.records[recordIndex].ResponseCompactField = responseShape.CompactField
			}
			r.mu.Unlock()
		}}
	}
	return resp, err
}

func (r *nativeLiveHTTPRecorder) Snapshot() []nativeLiveRequestEvidence {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]nativeLiveRequestEvidence(nil), r.records...)
}

// CodexSnapshot excludes auth-refresh traffic from the four-request proof while
// retaining it in Snapshot for failure diagnosis.
func (r *nativeLiveHTTPRecorder) CodexSnapshot() []nativeLiveRequestEvidence {
	r.mu.Lock()
	defer r.mu.Unlock()
	proof := make([]nativeLiveRequestEvidence, 0, len(r.records))
	for _, record := range r.records {
		if record.Phase == "normal" || record.Phase == "compaction" {
			proof = append(proof, record)
		}
	}
	return proof
}

func nativeLiveRequestPhase(req *http.Request, evidence nativeLiveRequestEvidence) string {
	if req != nil && req.URL != nil && strings.Contains(req.URL.Path, "/oauth/token") {
		return "auth_refresh"
	}
	if evidence.Compaction {
		return "compaction"
	}
	if evidence.Model != "" {
		return "normal"
	}
	return "unknown"
}

func nativeLiveStatusCategory(status int) string {
	switch {
	case status == 0:
		return "transport_failure"
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "authentication_failure"
	case status == http.StatusTooManyRequests:
		return "rate_limited"
	case status == http.StatusBadRequest || status == http.StatusUnprocessableEntity:
		return "request_rejected"
	case status == http.StatusNotFound:
		return "endpoint_or_model_not_found"
	case status >= 400 && status < 500:
		return "provider_client_failure"
	case status >= 500:
		return "provider_server_failure"
	default:
		return "success"
	}
}

func nativeLiveProviderErrorMetadata(body []byte) (string, string, string) {
	var envelope struct {
		Error struct {
			Code  string `json:"code"`
			Type  string `json:"type"`
			Param string `json:"param"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return "", "", ""
	}
	return nativeLiveAllowlistedEnum(envelope.Error.Code, nativeLiveProviderCodes),
		nativeLiveAllowlistedEnum(envelope.Error.Type, nativeLiveProviderTypes),
		nativeLiveAllowlistedParam(envelope.Error.Param)
}

var nativeLiveProviderParams = map[string]struct{}{
	"include": {}, "input": {}, "instructions": {}, "metadata": {}, "model": {},
	"parallel_tool_calls": {}, "previous_response_id": {}, "prompt_cache_key": {},
	"reasoning": {}, "store": {}, "stream": {}, "text": {}, "tool_choice": {}, "tools": {},
}

func nativeLiveAllowlistedParam(value string) string {
	value = nativeLiveAllowlistedEnum(value, nativeLiveProviderParams)
	return value
}

func nativeLiveAllowlistedEnum(value string, allowlist map[string]struct{}) string {
	value = strings.TrimSpace(value)
	if len(value) == 0 || len(value) > 64 {
		return ""
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' && char != '-' && char != '.' {
			return ""
		}
	}
	if _, ok := allowlist[value]; !ok {
		return ""
	}
	return value
}

type nativeLiveResponseBody struct {
	body       io.ReadCloser
	onComplete func([]byte)
	mu         sync.Mutex
	captured   []byte
	completed  bool
}

func (b *nativeLiveResponseBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if n > 0 {
		b.mu.Lock()
		if len(b.captured) < nativeLiveResponseCaptureLimit {
			remaining := min(n, nativeLiveResponseCaptureLimit-len(b.captured))
			b.captured = append(b.captured, p[:remaining]...)
		}
		b.mu.Unlock()
	}
	if err != nil {
		b.complete()
	}
	return n, err
}

func (b *nativeLiveResponseBody) Close() error {
	err := b.body.Close()
	b.complete()
	return err
}

func (b *nativeLiveResponseBody) complete() {
	b.mu.Lock()
	if b.completed {
		b.mu.Unlock()
		return
	}
	b.completed = true
	captured := append([]byte(nil), b.captured...)
	b.captured = nil
	b.mu.Unlock()
	if b.onComplete != nil {
		b.onComplete(captured)
	}
}

type nativeLiveCompactionResponseEvidence struct {
	Object         string
	ResponseStatus string
	TopLevelFields []string
	OutputShape    string
	OutputTypes    map[string]int
	UsagePresent   bool
	CompactField   bool
}

// nativeLiveCompactionResponseShape records structural metadata only. Response
// values, including encrypted content and text, never enter the proof record.
func nativeLiveCompactionResponseShape(body []byte) nativeLiveCompactionResponseEvidence {
	shape := nativeLiveCompactionResponseEvidence{OutputTypes: make(map[string]int)}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil || envelope == nil {
		return shape
	}
	for key := range envelope {
		shape.TopLevelFields = append(shape.TopLevelFields, key)
	}
	sort.Strings(shape.TopLevelFields)
	var object string
	if json.Unmarshal(envelope["object"], &object) == nil && object == "response.compaction" {
		shape.Object = object
	}
	var status string
	if json.Unmarshal(envelope["status"], &status) == nil {
		switch status {
		case "completed", "in_progress", "incomplete", "failed":
			shape.ResponseStatus = status
		}
	}
	_, shape.UsagePresent = envelope["usage"]
	if compact, ok := envelope["compact"]; ok && nativeLiveJSONObject(compact) {
		shape.CompactField = true
		shape.OutputShape = "compact_object"
		nativeLiveCountResponseType(compact, shape.OutputTypes)
		return shape
	}
	output, ok := envelope["output"]
	if !ok {
		return shape
	}
	trimmed := strings.TrimSpace(string(output))
	if strings.HasPrefix(trimmed, "[") {
		shape.OutputShape = "array"
		var items []json.RawMessage
		if json.Unmarshal(output, &items) == nil {
			for _, item := range items {
				nativeLiveCountResponseType(item, shape.OutputTypes)
			}
		}
	} else if strings.HasPrefix(trimmed, "{") {
		shape.OutputShape = "object"
		nativeLiveCountResponseType(output, shape.OutputTypes)
	}
	return shape
}

func nativeLiveJSONObject(raw []byte) bool {
	trimmed := strings.TrimSpace(string(raw))
	var object map[string]json.RawMessage
	return strings.HasPrefix(trimmed, "{") && json.Unmarshal(raw, &object) == nil && object != nil
}

func nativeLiveCountResponseType(raw []byte, counts map[string]int) {
	var item map[string]json.RawMessage
	if json.Unmarshal(raw, &item) != nil {
		return
	}
	var typ string
	if json.Unmarshal(item["type"], &typ) != nil || typ == "" {
		typ = "unknown"
	}
	counts[typ]++
}

func nativeLiveRequestShape(body []byte, expectedTail string) nativeLiveRequestEvidence {
	var payload struct {
		Model              string                     `json:"model"`
		Input              []json.RawMessage          `json:"input"`
		Tools              []json.RawMessage          `json:"tools"`
		Reasoning          map[string]json.RawMessage `json:"reasoning"`
		Include            []string                   `json:"include"`
		PreviousResponseID string                     `json:"previous_response_id"`
		Metadata           map[string]string          `json:"metadata"`
	}
	evidence := nativeLiveRequestEvidence{}
	if json.Unmarshal(body, &payload) != nil {
		return evidence
	}
	evidence.Model = payload.Model
	evidence.TopLevelFields = nativeLiveTopLevelFields(body)
	evidence.InputTypeCounts = make(map[string]int)
	evidence.HasReasoning = payload.Reasoning != nil
	_, evidence.ReasoningEffortPresent = payload.Reasoning["effort"]
	for _, field := range payload.Include {
		if field == "reasoning.encrypted_content" {
			evidence.IncludeEncrypted = true
		}
	}
	evidence.ToolCount = len(payload.Tools)
	evidence.PreviousResponseID = payload.PreviousResponseID != ""
	for key := range payload.Metadata {
		if _, ok := map[string]struct{}{"compaction": {}, "phase": {}, "reasoning_continuity": {}}[key]; ok {
			evidence.MetadataKeys = append(evidence.MetadataKeys, key)
		}
	}
	sort.Strings(evidence.MetadataKeys)
	evidence.MarkerLeaked = bytes.Contains(body, []byte(nativeContinuityMarkerKey))
	evidence.TailPreserved = expectedTail != "" && bytes.Contains(body, []byte(expectedTail))
	for i, raw := range payload.Input {
		var item struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		if item.Type == "compaction_summary" {
			evidence.HasCheckpoint = true
			evidence.HasSummary = true
		}
		if item.Type == "compaction" && i == len(payload.Input)-1 {
			evidence.Compaction = true
			evidence.TriggerCount++
		}
		if item.Type == "" {
			item.Type = "unknown"
		}
		evidence.InputTypeCounts[item.Type]++
	}
	evidence.CompactionTailExcluded = evidence.HasCheckpoint && expectedTail != "" && !evidence.TailPreserved
	return evidence
}

func nativeLiveTopLevelFields(body []byte) []string {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return nil
	}
	out := make([]string, 0, len(fields))
	for field := range fields {
		out = append(out, field)
	}
	sort.Strings(out)
	return out
}

func TestNativeLiveRequestShapeDoesNotRetainOpaqueEvidence(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"gpt-5.3-codex-spark","reasoning":{"summary":"auto"},"include":["reasoning.encrypted_content"],"input":[{"type":"message","content":[{"text":"State one short deterministic fact for the next turn."}]},{"type":"compaction_summary","encrypted_content":"opaque"}]}`)
	shape := nativeLiveRequestShape(body, nativeContextLiveTail)
	if shape.Compaction || shape.TriggerCount != 0 || !shape.HasCheckpoint || !shape.CompactionTailExcluded {
		t.Fatalf("shape = %#v", shape)
	}
	if shape.Model != nativeContextLiveModel || shape.MarkerLeaked {
		t.Fatalf("safe shape metadata = %#v", shape)
	}
	if !shape.HasReasoning || !shape.IncludeEncrypted || shape.InputTypeCounts["message"] != 1 {
		t.Fatalf("reasoning shape = %#v", shape)
	}
}

func TestNativeLiveHTTPRecorderForwardsAndStoresOnlySafeShape(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	recorder := newNativeLiveHTTPRecorder(nativeContextLiveTail, http.DefaultTransport)
	request, err := http.NewRequest(http.MethodPost, server.URL, bytes.NewReader([]byte(`{"model":"gpt-5.3-codex-spark","input":[{"type":"message","content":[{"text":"LIVE_NATIVE_CONTEXT_TAIL_7B2A: answer with one short word."}]}]}`)))
	if err != nil {
		t.Fatal(err)
	}
	response, err := recorder.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	proof := recorder.Snapshot()
	if len(proof) != 1 || proof[0].StatusCode != http.StatusNoContent || !proof[0].TailPreserved {
		t.Fatalf("proof = %#v", proof)
	}
}

func TestNativeLiveHTTPRecorderCapturesCompactResponseShapeWithoutConsumingBody(t *testing.T) {
	t.Parallel()
	const secret = "opaque-response-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"response-id","object":"response.compaction","created_at":1,"output":[{"type":"message","role":"user","status":"completed","content":[{"type":"input_text","text":"retained"}]},{"type":"compaction_summary","id":"item-id","encrypted_content":"`+secret+`"}],"usage":{"input_tokens":1}}`)
	}))
	defer server.Close()
	recorder := newNativeLiveHTTPRecorder("tail", http.DefaultTransport)
	request, err := http.NewRequest(http.MethodPost, server.URL+"/responses/compact", strings.NewReader(`{"model":"model","input":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	response, err := recorder.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if !bytes.Contains(body, []byte(secret)) {
		t.Fatal("recorder did not forward the complete response body")
	}
	proof := recorder.Snapshot()
	if len(proof) != 1 || proof[0].ResponseObject != "response.compaction" || proof[0].ResponseOutputShape != "array" || proof[0].ResponseOutputTypes["compaction_summary"] != 1 || proof[0].ResponseOutputTypes["message"] != 1 || !proof[0].ResponseUsagePresent {
		t.Fatalf("response shape = %#v", proof)
	}
	if strings.Contains(fmt.Sprintf("%#v", proof), secret) {
		t.Fatal("safe response proof retained opaque response content")
	}
}

func TestNativeLiveHTTPRecorderRedactsProviderSecretsAndPreservesResponse(t *testing.T) {
	t.Parallel()
	const secret = "PROMPT_SECRET_9f2a bearer-secret-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-secret-token" {
			t.Errorf("authorization header was not proxied")
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"model_not_found","type":"invalid_request_error","message":"` + secret + `"}}`))
	}))
	defer server.Close()
	recorder := newNativeLiveHTTPRecorder(nativeContextLiveTail, http.DefaultTransport)
	request, err := http.NewRequest(http.MethodPost, server.URL+"/responses", strings.NewReader(`{"model":"gpt-5.3-codex-spark","input":[{"type":"message"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer synthetic-secret-token")
	response, err := recorder.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if !bytes.Contains(body, []byte(secret)) {
		t.Fatal("recorder did not proxy the response body unchanged")
	}
	proof := recorder.Snapshot()
	if len(proof) != 1 || proof[0].StatusCode != http.StatusBadRequest || proof[0].Category != "request_rejected" || proof[0].ProviderCode != "model_not_found" || proof[0].ProviderType != "invalid_request_error" {
		t.Fatalf("safe proof = %#v", proof)
	}
	if strings.Contains(strings.Join([]string{proof[0].ProviderCode, proof[0].ProviderType}, " "), secret) {
		t.Fatal("safe proof retained provider secret")
	}
}

func TestNativeLiveProviderMetadataAllowlistRejectsSecrets(t *testing.T) {
	t.Parallel()
	code, typ, param := nativeLiveProviderErrorMetadata([]byte(`{"error":{"code":"PROMPT_SECRET","type":"invalid_request_error","param":"reasoning","message":"do not retain"}}`))
	if code != "" || typ != "invalid_request_error" || param != "reasoning" {
		t.Fatalf("metadata = code %q type %q param %q", code, typ, param)
	}
}

func TestNativeLiveProviderMetadataRejectsUnallowlistedParam(t *testing.T) {
	t.Parallel()
	_, _, param := nativeLiveProviderErrorMetadata([]byte(`{"error":{"param":"PROMPT_SECRET"}}`))
	if param != "" {
		t.Fatalf("param = %q, want redacted", param)
	}
}

func TestNativeLiveRequestShapeContainsNoPayloadValues(t *testing.T) {
	t.Parallel()
	shape := nativeLiveRequestShape([]byte(`{"model":"gpt-5.3-codex-spark","instructions":"secret instruction","reasoning":{"summary":"auto"},"include":["reasoning.encrypted_content"],"metadata":{"phase":"secret"},"input":[{"type":"message","role":"user","content":"secret prompt"}],"tools":[{"type":"function","name":"secret-tool"}]}`), "secret prompt")
	joined := fmt.Sprintf("%#v", shape)
	for _, secret := range []string{"secret instruction", "secret prompt", "secret-tool"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("shape retained payload value %q: %s", secret, joined)
		}
	}
	if !shape.HasReasoning || !shape.IncludeEncrypted || shape.ToolCount != 1 || shape.InputTypeCounts["message"] != 1 || len(shape.MetadataKeys) != 1 || shape.MetadataKeys[0] != "phase" {
		t.Fatalf("shape = %#v", shape)
	}
}
