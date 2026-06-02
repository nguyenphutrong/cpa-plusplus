package executor

import (
	"context"
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
