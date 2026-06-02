package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"strings"
	"testing"

	kiroclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/kiro/claude"
	openairesponses "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/openai/responses"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestBuildKiroRequestPayloadFromOpenAIResponsesCompact(t *testing.T) {
	in := []byte(`{"model":"claude-sonnet-4.5","input":"Summarize this conversation","max_output_tokens":123}`)

	out := buildKiroRequestPayload(sdktranslator.FormatOpenAIResponse, "claude-sonnet-4.5", "claude-sonnet-4.5", "arn:aws:kiro", in, false, nil, nil)

	if !gjson.ValidBytes(out) {
		t.Fatalf("invalid JSON: %s", string(out))
	}
	if gjson.GetBytes(out, "input").Exists() {
		t.Fatalf("unexpected top-level input in Kiro payload: %s", string(out))
	}
	if gjson.GetBytes(out, "messages").Exists() {
		t.Fatalf("unexpected top-level messages in Kiro payload: %s", string(out))
	}
	if got := gjson.GetBytes(out, "conversationState.chatTriggerType").String(); got != "MANUAL" {
		t.Fatalf("chatTriggerType = %q, want MANUAL", got)
	}
	if got := gjson.GetBytes(out, "conversationState.currentMessage.userInputMessage.modelId").String(); got != "claude-sonnet-4.5" {
		t.Fatalf("modelId = %q, want claude-sonnet-4.5", got)
	}
	if got := gjson.GetBytes(out, "conversationState.currentMessage.userInputMessage.origin").String(); got != "AI_EDITOR" {
		t.Fatalf("origin = %q, want AI_EDITOR", got)
	}
	if got := gjson.GetBytes(out, "profileArn").String(); got != "arn:aws:kiro" {
		t.Fatalf("profileArn = %q, want arn:aws:kiro", got)
	}
	if got := gjson.GetBytes(out, "inferenceConfig.maxTokens").Int(); got != 123 {
		t.Fatalf("maxTokens = %d, want 123", got)
	}
	content := gjson.GetBytes(out, "conversationState.currentMessage.userInputMessage.content").String()
	if !strings.Contains(content, "Summarize this conversation") {
		t.Fatalf("content does not include compact input: %q", content)
	}
}

func TestBuildKiroRequestPayloadFromOpenAIChat(t *testing.T) {
	in := []byte(`{"model":"claude-sonnet-4.5","messages":[{"role":"user","content":"Hello Kiro"}]}`)

	out := buildKiroRequestPayload(sdktranslator.FormatOpenAI, "claude-sonnet-4.5", "claude-sonnet-4.5", "", in, false, nil, nil)

	if gjson.GetBytes(out, "messages").Exists() {
		t.Fatalf("unexpected top-level messages in Kiro payload: %s", string(out))
	}
	content := gjson.GetBytes(out, "conversationState.currentMessage.userInputMessage.content").String()
	if !strings.Contains(content, "Hello Kiro") {
		t.Fatalf("content does not include user message: %q", content)
	}
}

func TestNormalizeKiroModelPreservesKiroModelIDs(t *testing.T) {
	tests := map[string]string{
		"deepseek-3.2":                   "deepseek-3.2",
		"qwen3-coder-next":               "qwen3-coder-next",
		"claude-sonnet-4":                "claude-sonnet-4",
		"kiro-claude-sonnet-4-5-agentic": "claude-sonnet-4.5",
		"sonnet":                         "claude-sonnet-4.5",
	}

	for input, want := range tests {
		if got := normalizeKiroModel(input); got != want {
			t.Fatalf("normalizeKiroModel(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestExtractKiroTextPreservesWhitespace(t *testing.T) {
	cases := map[string]string{
		`{"assistantResponseEvent":{"content":" world"}}`:       " world",
		`{"assistantResponseEvent":{"content":"line1\nline2"}}`: "line1\nline2",
		`{"content":"\n\n"}`:                "\n\n",
		`{"content":"  trailing  "}`:        "  trailing  ",
		`{"usageEvent":{"inputTokens":5}}`:  "",
		`{"content":"","text":" fallback"}`: " fallback",
	}
	for payload, want := range cases {
		if got := extractKiroText([]byte(payload)); got != want {
			t.Fatalf("extractKiroText(%s) = %q, want %q", payload, got, want)
		}
	}
}

func TestStreamKiroTextDeltasConcatenateVerbatim(t *testing.T) {
	ctx := context.Background()
	model := "claude-sonnet-4.5"
	original := []byte(`{"model":"claude-sonnet-4.5","input":"hi","stream":true}`)

	deltas := []string{"Hello", " world.", "\n\nNext", " line"}

	var paramChat any
	var paramResp any
	var all []byte
	collect := func(event []byte) {
		for _, chunk := range translateKiroStreamToResponses(ctx, model, original, original, event, &paramChat, &paramResp) {
			all = append(all, chunk...)
		}
	}

	collect(kiroclaude.BuildClaudeMessageStartEvent(model, 0))
	collect(kiroclaude.BuildClaudeContentBlockStartEvent(0, "text", "", ""))
	for _, d := range deltas {
		collect(kiroclaude.BuildClaudeStreamEvent(d, 0))
	}
	collect(kiroclaude.BuildClaudeContentBlockStopEvent(0))
	collect(kiroclaude.BuildClaudeMessageDeltaEvent("end_turn", usage.Detail{OutputTokens: 1, TotalTokens: 1}))
	collect(kiroclaude.BuildClaudeMessageStopOnlyEvent())
	for _, chunk := range openairesponses.ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, model, original, original, []byte("[DONE]"), &paramResp) {
		all = append(all, chunk...)
	}

	var text strings.Builder
	for _, line := range strings.Split(string(all), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if gjson.Get(data, "type").String() == "response.output_text.delta" {
			text.WriteString(gjson.Get(data, "delta").String())
		}
	}
	if got, want := text.String(), strings.Join(deltas, ""); got != want {
		t.Fatalf("reconstructed text = %q, want %q", got, want)
	}
}

func TestTranslateKiroStreamToResponsesEmitsCompleted(t *testing.T) {
	ctx := context.Background()
	model := "claude-sonnet-4.5"
	original := []byte(`{"model":"claude-sonnet-4.5","input":"hi","stream":true}`)

	var paramChat any
	var paramResp any
	var all []byte
	collect := func(event []byte) {
		for _, chunk := range translateKiroStreamToResponses(ctx, model, original, original, event, &paramChat, &paramResp) {
			all = append(all, chunk...)
		}
	}

	collect(kiroclaude.BuildClaudeMessageStartEvent(model, 0))
	collect(kiroclaude.BuildClaudeContentBlockStartEvent(0, "text", "", ""))
	collect(kiroclaude.BuildClaudeStreamEvent("Hello", 0))
	collect(kiroclaude.BuildClaudeContentBlockStopEvent(0))
	collect(kiroclaude.BuildClaudeMessageDeltaEvent("end_turn", usage.Detail{OutputTokens: 1, TotalTokens: 1}))
	collect(kiroclaude.BuildClaudeMessageStopOnlyEvent())

	for _, chunk := range openairesponses.ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, model, original, original, []byte("[DONE]"), &paramResp) {
		all = append(all, chunk...)
	}

	got := string(all)
	if !strings.Contains(got, "response.completed") {
		t.Fatalf("expected response.completed event, got: %s", got)
	}
	if !strings.Contains(got, "response.output_text.delta") {
		t.Fatalf("expected response.output_text.delta event, got: %s", got)
	}
}

func buildKiroEventFrame(eventType string, payload string) []byte {
	headerName := ":event-type"
	header := []byte{byte(len(headerName))}
	header = append(header, headerName...)
	header = append(header, 7)
	header = append(header, byte(len(eventType)>>8), byte(len(eventType)))
	header = append(header, eventType...)

	body := []byte(payload)
	totalLen := 12 + len(header) + len(body) + 4
	frame := make([]byte, 0, totalLen)
	var prelude [12]byte
	binary.BigEndian.PutUint32(prelude[0:4], uint32(totalLen))
	binary.BigEndian.PutUint32(prelude[4:8], uint32(len(header)))
	frame = append(frame, prelude[:]...)
	frame = append(frame, header...)
	frame = append(frame, body...)
	frame = append(frame, 0, 0, 0, 0) // message CRC (unused by reader)
	return frame
}

func TestReadKiroEventFrameParsesEventType(t *testing.T) {
	frame := buildKiroEventFrame("toolUseEvent", `{"toolUseId":"call_1","name":"search","input":"{\"q\":1}"}`)
	reader := bufio.NewReader(bytes.NewReader(frame))

	eventType, payload, err := readKiroEventFrame(reader)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eventType != "toolUseEvent" {
		t.Fatalf("eventType = %q, want toolUseEvent", eventType)
	}
	if got := gjson.GetBytes(payload, "toolUseId").String(); got != "call_1" {
		t.Fatalf("toolUseId = %q, want call_1", got)
	}
}

func TestExtractKiroToolUse(t *testing.T) {
	id, name, input := extractKiroToolUse([]byte(`{"toolUseId":"call_1","name":"search","input":"{\"q\":1}"}`))
	if id != "call_1" || name != "search" || input != `{"q":1}` {
		t.Fatalf("flat = (%q,%q,%q)", id, name, input)
	}

	id, name, input = extractKiroToolUse([]byte(`{"toolUseEvent":{"toolUseId":"call_2","name":"read","input":{"path":"/a"}}}`))
	if id != "call_2" || name != "read" || input != `{"path":"/a"}` {
		t.Fatalf("nested = (%q,%q,%q)", id, name, input)
	}

	if !isKiroToolUseEvent("toolUseEvent", nil) {
		t.Fatalf("expected toolUseEvent header to be detected")
	}
	if !isKiroToolUseEvent("", []byte(`{"toolUseId":"x"}`)) {
		t.Fatalf("expected toolUseId payload to be detected")
	}
	if isKiroToolUseEvent("assistantResponseEvent", []byte(`{"content":"hi"}`)) {
		t.Fatalf("text event should not be detected as tool use")
	}
}

func TestParseKiroEventStreamPayloadsNativeToolUse(t *testing.T) {
	var raw []byte
	raw = append(raw, buildKiroEventFrame("assistantResponseEvent", `{"content":"Let me check. "}`)...)
	raw = append(raw, buildKiroEventFrame("toolUseEvent", `{"toolUseId":"call_1","name":"get_weather","input":"{\"city\":"}`)...)
	raw = append(raw, buildKiroEventFrame("toolUseEvent", `{"toolUseId":"call_1","input":"\"Hanoi\"}"}`)...)
	raw = append(raw, buildKiroEventFrame("messageStopEvent", `{}`)...)

	content, toolUses, _, stopReason := parseKiroEventStreamPayloads(raw)
	if content != "Let me check. " {
		t.Fatalf("content = %q", content)
	}
	if stopReason != "tool_use" {
		t.Fatalf("stopReason = %q, want tool_use", stopReason)
	}
	if len(toolUses) != 1 {
		t.Fatalf("len(toolUses) = %d, want 1", len(toolUses))
	}
	if toolUses[0].ToolUseID != "call_1" || toolUses[0].Name != "get_weather" {
		t.Fatalf("toolUse identity = %+v", toolUses[0])
	}
	if got, _ := toolUses[0].Input["city"].(string); got != "Hanoi" {
		t.Fatalf("tool input city = %q, want Hanoi", got)
	}
}

func TestStreamKiroToolUseProducesFunctionCall(t *testing.T) {
	ctx := context.Background()
	model := "claude-sonnet-4.5"
	original := []byte(`{"model":"claude-sonnet-4.5","input":"weather?","stream":true}`)

	var paramChat any
	var paramResp any
	var all []byte
	collect := func(event []byte) {
		for _, chunk := range translateKiroStreamToResponses(ctx, model, original, original, event, &paramChat, &paramResp) {
			all = append(all, chunk...)
		}
	}

	collect(kiroclaude.BuildClaudeMessageStartEvent(model, 0))
	collect(kiroclaude.BuildClaudeContentBlockStartEvent(0, "text", "", ""))
	collect(kiroclaude.BuildClaudeContentBlockStartEvent(1, "tool_use", "call_1", "get_weather"))
	collect(kiroclaude.BuildClaudeInputJsonDeltaEvent(`{"city":"Hanoi"}`, 1))
	collect(kiroclaude.BuildClaudeContentBlockStopEvent(0))
	collect(kiroclaude.BuildClaudeContentBlockStopEvent(1))
	collect(kiroclaude.BuildClaudeMessageDeltaEvent("tool_use", usage.Detail{OutputTokens: 1, TotalTokens: 1}))
	collect(kiroclaude.BuildClaudeMessageStopOnlyEvent())
	for _, chunk := range openairesponses.ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, model, original, original, []byte("[DONE]"), &paramResp) {
		all = append(all, chunk...)
	}

	got := string(all)
	if !strings.Contains(got, "response.function_call_arguments.done") {
		t.Fatalf("expected function_call_arguments.done, got: %s", got)
	}
	if !strings.Contains(got, "get_weather") {
		t.Fatalf("expected tool name in output, got: %s", got)
	}
	if !strings.Contains(got, "Hanoi") {
		t.Fatalf("expected tool arguments in output, got: %s", got)
	}
	if !strings.Contains(got, "response.completed") {
		t.Fatalf("expected response.completed, got: %s", got)
	}
}

// runKiroStreamThroughResponses drives the real executor stream loop (streamKiroClaudeEvents)
// over raw Kiro frames and pipes the emitted Claude SSE through the OpenAI Responses chain,
// returning the concatenated Responses SSE output.
func runKiroStreamThroughResponses(t *testing.T, ctx context.Context, model string, original, raw []byte) string {
	t.Helper()
	var paramChat any
	var paramResp any
	var all []byte
	send := func(event []byte) bool {
		for _, chunk := range translateKiroStreamToResponses(ctx, model, original, original, event, &paramChat, &paramResp) {
			all = append(all, chunk...)
		}
		return true
	}
	send(kiroclaude.BuildClaudeMessageStartEvent(model, 0))
	reader := bufio.NewReader(bytes.NewReader(raw))
	usageDetail, stopReason, ok := streamKiroClaudeEvents(reader, send, nil, nil, nil)
	if !ok {
		t.Fatalf("stream aborted unexpectedly")
	}
	send(kiroclaude.BuildClaudeMessageDeltaEvent(stopReason, usageDetail))
	send(kiroclaude.BuildClaudeMessageStopOnlyEvent())
	for _, chunk := range openairesponses.ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, model, original, original, []byte("[DONE]"), &paramResp) {
		all = append(all, chunk...)
	}
	return string(all)
}

func TestExtractKiroReasoning(t *testing.T) {
	cases := map[string]string{
		`{"text":"step 1 "}`:                        "step 1 ",
		`{"content":"thinking..."}`:                 "thinking...",
		`{"reasoningContentEvent":{"text":"deep"}}`: "deep",
		`{"reasoningContentEvent":{"content":"x"}}`: "x",
		`{"foo":"bar"}`:                             "",
	}
	for payload, want := range cases {
		if got := extractKiroReasoning([]byte(payload)); got != want {
			t.Fatalf("extractKiroReasoning(%s) = %q, want %q", payload, got, want)
		}
	}
	if !isKiroReasoningEvent("reasoningContentEvent") {
		t.Fatalf("expected reasoningContentEvent to be detected")
	}
	if isKiroReasoningEvent("assistantResponseEvent") {
		t.Fatalf("assistantResponseEvent must not be a reasoning event")
	}
}

func TestStreamKiroReasoningThenTextAndTool(t *testing.T) {
	ctx := context.Background()
	model := "claude-sonnet-4.5"
	original := []byte(`{"model":"claude-sonnet-4.5","input":"weather?","stream":true,"reasoning":{"effort":"low"}}`)

	var raw []byte
	raw = append(raw, buildKiroEventFrame("reasoningContentEvent", `{"text":"Let me think. "}`)...)
	raw = append(raw, buildKiroEventFrame("reasoningContentEvent", `{"text":"Need weather."}`)...)
	raw = append(raw, buildKiroEventFrame("assistantResponseEvent", `{"content":"Checking now. "}`)...)
	raw = append(raw, buildKiroEventFrame("toolUseEvent", `{"toolUseId":"call_1","name":"get_weather","input":"{\"city\":\"Hanoi\"}"}`)...)
	raw = append(raw, buildKiroEventFrame("messageStopEvent", `{}`)...)

	// Drive the same Claude-event pipeline the executor uses, then translate to Responses.
	all := runKiroStreamThroughResponses(t, ctx, model, original, raw)

	if !strings.Contains(all, "response.reasoning_summary_text.delta") {
		t.Fatalf("expected reasoning delta event, got: %s", all)
	}
	if !strings.Contains(all, "Let me think. Need weather.") {
		t.Fatalf("expected concatenated reasoning text, got: %s", all)
	}
	if !strings.Contains(all, "response.output_text.delta") || !strings.Contains(all, "Checking now. ") {
		t.Fatalf("expected text delta, got: %s", all)
	}
	if !strings.Contains(all, "get_weather") || !strings.Contains(all, "Hanoi") {
		t.Fatalf("expected tool call with args, got: %s", all)
	}
	if !strings.Contains(all, "response.completed") {
		t.Fatalf("expected response.completed, got: %s", all)
	}
}

func TestParseKiroEventStreamPayloadsReasoningBecomesThinking(t *testing.T) {
	var raw []byte
	raw = append(raw, buildKiroEventFrame("reasoningContentEvent", `{"text":"reasoning here"}`)...)
	raw = append(raw, buildKiroEventFrame("assistantResponseEvent", `{"content":"final answer"}`)...)
	raw = append(raw, buildKiroEventFrame("messageStopEvent", `{}`)...)

	content, _, _, _ := parseKiroEventStreamPayloads(raw)
	if !strings.HasPrefix(content, "<thinking>reasoning here</thinking>") {
		t.Fatalf("reasoning not wrapped as thinking block: %q", content)
	}
	if !strings.Contains(content, "final answer") {
		t.Fatalf("answer text missing: %q", content)
	}
}

func TestKiroToolIndexMapSurvivesLeadingThinkingBlock(t *testing.T) {
	ctx := context.Background()
	model := "claude-sonnet-4.5"
	original := []byte(`{"model":"claude-sonnet-4.5","input":"x","stream":true}`)

	// thinking at block 0, text at block 1, two tool blocks at 2 and 3.
	var paramChat any
	var paramResp any
	var all []byte
	collect := func(event []byte) {
		for _, chunk := range translateKiroStreamToResponses(ctx, model, original, original, event, &paramChat, &paramResp) {
			all = append(all, chunk...)
		}
	}
	collect(kiroclaude.BuildClaudeMessageStartEvent(model, 0))
	collect(kiroclaude.BuildClaudeContentBlockStartEvent(0, "thinking", "", ""))
	collect(kiroclaude.BuildClaudeThinkingDeltaEvent("reason", 0))
	collect(kiroclaude.BuildClaudeContentBlockStopEvent(0))
	collect(kiroclaude.BuildClaudeContentBlockStartEvent(2, "tool_use", "call_a", "alpha"))
	collect(kiroclaude.BuildClaudeInputJsonDeltaEvent(`{"a":1}`, 2))
	collect(kiroclaude.BuildClaudeContentBlockStopEvent(2))
	collect(kiroclaude.BuildClaudeContentBlockStartEvent(3, "tool_use", "call_b", "beta"))
	collect(kiroclaude.BuildClaudeInputJsonDeltaEvent(`{"b":2}`, 3))
	collect(kiroclaude.BuildClaudeContentBlockStopEvent(3))
	collect(kiroclaude.BuildClaudeMessageDeltaEvent("tool_use", usage.Detail{OutputTokens: 1, TotalTokens: 1}))
	collect(kiroclaude.BuildClaudeMessageStopOnlyEvent())
	for _, chunk := range openairesponses.ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, model, original, original, []byte("[DONE]"), &paramResp) {
		all = append(all, chunk...)
	}

	got := string(all)
	// SSE payloads JSON-escape the argument quotes, so match the escaped forms.
	if !strings.Contains(got, `{\"a\":1}`) || !strings.Contains(got, "alpha") {
		t.Fatalf("first tool args/name missing: %s", got)
	}
	if !strings.Contains(got, `{\"b\":2}`) || !strings.Contains(got, "beta") {
		t.Fatalf("second tool args/name missing (index mapping likely broken): %s", got)
	}
}

func TestStreamKiroToolOnlyTurnEmitsNoTextBlock(t *testing.T) {
	var raw []byte
	raw = append(raw, buildKiroEventFrame("toolUseEvent", `{"toolUseId":"call_1","name":"get_weather","input":"{\"city\":\"Hanoi\"}"}`)...)
	raw = append(raw, buildKiroEventFrame("messageStopEvent", `{}`)...)

	var events []string
	send := func(event []byte) bool {
		events = append(events, string(event))
		return true
	}
	reader := bufio.NewReader(bytes.NewReader(raw))
	usageDetail, stopReason, ok := streamKiroClaudeEvents(reader, send, nil, nil, nil)
	if !ok {
		t.Fatalf("stream aborted")
	}
	if stopReason != "tool_use" {
		t.Fatalf("stopReason = %q, want tool_use", stopReason)
	}
	_ = usageDetail
	for _, e := range events {
		if strings.Contains(e, "content_block_start") && strings.Contains(e, `"type":"text"`) {
			t.Fatalf("unexpected empty text block emitted for tool-only turn: %s", e)
		}
	}
	// The single tool block must start at index 0 since no text block precedes it.
	foundToolAtZero := false
	for _, e := range events {
		if strings.Contains(e, "content_block_start") && strings.Contains(e, `"type":"tool_use"`) {
			if strings.Contains(e, `"index":0`) {
				foundToolAtZero = true
			}
		}
	}
	if !foundToolAtZero {
		t.Fatalf("expected tool block at index 0, events: %v", events)
	}
}

func TestStreamKiroUsageInputTokensPropagate(t *testing.T) {
	var raw []byte
	raw = append(raw, buildKiroEventFrame("assistantResponseEvent", `{"content":"hello"}`)...)
	raw = append(raw, buildKiroEventFrame("metricsEvent", `{"inputTokens":42,"outputTokens":7,"totalTokens":49}`)...)
	raw = append(raw, buildKiroEventFrame("messageStopEvent", `{}`)...)

	send := func(event []byte) bool { return true }
	reader := bufio.NewReader(bytes.NewReader(raw))
	usageDetail, _, ok := streamKiroClaudeEvents(reader, send, nil, nil, nil)
	if !ok {
		t.Fatalf("stream aborted")
	}
	if usageDetail.InputTokens != 42 {
		t.Fatalf("InputTokens = %d, want 42", usageDetail.InputTokens)
	}
	if usageDetail.TotalTokens < 42 {
		t.Fatalf("TotalTokens = %d, want >= 42", usageDetail.TotalTokens)
	}
}

func TestExtractKiroUsageReadsMetricsEvent(t *testing.T) {
	detail := extractKiroUsage([]byte(`{"metricsEvent":{"inputTokens":11,"outputTokens":3,"totalTokens":14}}`))
	if detail.InputTokens != 11 || detail.OutputTokens != 3 || detail.TotalTokens != 14 {
		t.Fatalf("unexpected usage from metricsEvent: %+v", detail)
	}
}

func TestFinalizeCompactResponse(t *testing.T) {
	in := []byte(`{"id":"resp_1","object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15},"created_at":1234567890}`)

	out := finalizeCompactResponse(in)

	if !gjson.ValidBytes(out) {
		t.Fatalf("invalid JSON: %s", string(out))
	}
	if got := gjson.GetBytes(out, "object").String(); got != "response.compaction" {
		t.Fatalf("object = %q, want response.compaction", got)
	}
	if gjson.GetBytes(out, "output").Exists() {
		t.Fatalf("unexpected output in compact response: %s", string(out))
	}
	if gjson.GetBytes(out, "status").Exists() {
		t.Fatalf("unexpected status in compact response: %s", string(out))
	}
	if got := gjson.GetBytes(out, "usage.input_tokens").Int(); got != 10 {
		t.Fatalf("usage.input_tokens = %d, want 10", got)
	}
	if got := gjson.GetBytes(out, "usage.output_tokens").Int(); got != 5 {
		t.Fatalf("usage.output_tokens = %d, want 5", got)
	}
}
