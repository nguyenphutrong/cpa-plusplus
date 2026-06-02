package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	kiroclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/kiro/claude"
	kiroopenai "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/kiro/openai"
	openairesponses "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/openai/responses"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	kiroProvider       = "kiro"
	kiroDefaultRegion  = "us-east-1"
	kiroAgentMode      = "vibe"
	kiroScannerMaxSize = 10 << 20
)

type KiroExecutor struct {
	cfg *config.Config
}

type kiroEndpoint struct {
	URL    string
	Target string
	Name   string
	Origin string
}

func NewKiroExecutor(cfg *config.Config) *KiroExecutor {
	return &KiroExecutor{cfg: cfg}
}

func (e *KiroExecutor) Identifier() string { return kiroProvider }

func (e *KiroExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	token, _ := kiroCredentials(auth)
	if token == "" {
		return statusErr{code: http.StatusUnauthorized, msg: "missing kiro access token"}
	}
	applyKiroHeaders(req, auth, token, kiroEndpoint{})
	return nil
}

func (e *KiroExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("kiro executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(httpReq)
}

func (e *KiroExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	token, profileARN := kiroCredentials(auth)
	if token == "" {
		return resp, statusErr{code: http.StatusUnauthorized, msg: "missing kiro access token"}
	}
	model := normalizeKiroModel(req.Model)
	reporter := helps.NewUsageReporter(ctx, e.Identifier(), model, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("kiro")
	body := buildKiroRequestPayload(from, model, req.Model, profileARN, req.Payload, false, opts.Headers, opts.Metadata)

	for _, endpoint := range kiroEndpoints(auth, profileARN) {
		kiroBody, errDo := e.doKiroRequest(ctx, auth, token, endpoint, body)
		if errDo != nil {
			if se, ok := errDo.(interface{ StatusCode() int }); ok && se.StatusCode() == http.StatusTooManyRequests {
				err = errDo
				continue
			}
			err = errDo
			return resp, err
		}
		content, toolUses, detail, stopReason := parseKiroEventStreamPayloads(kiroBody)
		if detail.TotalTokens > 0 {
			reporter.Publish(ctx, detail)
		}
		reporter.EnsurePublished(ctx)
		claudeBody := kiroclaude.BuildClaudeResponse(content, toolUses, model, detail, stopReason)
		out := translateKiroNonStreamResponse(ctx, to, from, req.Model, opts.OriginalRequest, body, claudeBody)
		if opts.Alt == "responses/compact" {
			out = finalizeCompactResponse(out)
		}
		return cliproxyexecutor.Response{Payload: out}, nil
	}
	if err == nil {
		err = fmt.Errorf("kiro: all endpoints exhausted")
	}
	return resp, err
}

func (e *KiroExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	token, profileARN := kiroCredentials(auth)
	if token == "" {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "missing kiro access token"}
	}
	model := normalizeKiroModel(req.Model)
	reporter := helps.NewUsageReporter(ctx, e.Identifier(), model, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("kiro")
	body := buildKiroRequestPayload(from, model, req.Model, profileARN, req.Payload, true, opts.Headers, opts.Metadata)

	var lastErr error
	for _, endpoint := range kiroEndpoints(auth, profileARN) {
		httpResp, errDo := e.openKiroStream(ctx, auth, token, endpoint, body)
		if errDo != nil {
			lastErr = errDo
			if se, ok := errDo.(interface{ StatusCode() int }); ok && se.StatusCode() == http.StatusTooManyRequests {
				continue
			}
			return nil, errDo
		}
		out := make(chan cliproxyexecutor.StreamChunk)
		go e.streamKiroResponse(ctx, httpResp, out, reporter, to, from, req, opts, body)
		return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("kiro: all endpoints exhausted")
}

func (e *KiroExecutor) CountTokens(ctx context.Context, _ *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	enc, err := helps.TokenizerForModel(req.Model)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	count, err := helps.CountOpenAIChatTokens(enc, req.Payload)
	if err != nil {
		count = int64(len(req.Payload) / 4)
	}
	raw := helps.BuildOpenAIUsageJSON(count)
	out := sdktranslator.TranslateTokenCount(ctx, sdktranslator.FromString("kiro"), opts.SourceFormat, count, raw)
	return cliproxyexecutor.Response{Payload: out}, nil
}

func (e *KiroExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return sdkAuth.RefreshKiroToken(ctx, e.cfg, auth)
}

func (e *KiroExecutor) doKiroRequest(ctx context.Context, auth *cliproxyauth.Auth, token string, endpoint kiroEndpoint, body []byte) ([]byte, error) {
	httpResp, err := e.openKiroStream(ctx, auth, token, endpoint, body)
	if err != nil {
		return nil, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("kiro executor: close response body error: %v", errClose)
		}
	}()
	return io.ReadAll(httpResp.Body)
}

func (e *KiroExecutor) openKiroStream(ctx context.Context, auth *cliproxyauth.Auth, token string, endpoint kiroEndpoint, body []byte) (*http.Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	applyKiroHeaders(httpReq, auth, token, endpoint)
	recordKiroRequest(ctx, e.cfg, auth, endpoint.URL, httpReq.Header, body)
	httpResp, err := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("kiro executor: close response body error: %v", errClose)
		}
		return nil, statusErr{code: httpResp.StatusCode, msg: string(data)}
	}
	return httpResp, nil
}

func (e *KiroExecutor) streamKiroResponse(ctx context.Context, httpResp *http.Response, out chan<- cliproxyexecutor.StreamChunk, reporter *helps.UsageReporter, to, from sdktranslator.Format, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, requestBody []byte) {
	defer close(out)
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("kiro executor: close response body error: %v", errClose)
		}
		reporter.EnsurePublished(ctx)
	}()

	// Kiro streams Claude-format SSE events internally. The registry only knows how to
	// translate those into OpenAI Chat or Claude SSE; there is no direct openai-response
	// transform. For openai-response clients we therefore chain Claude SSE -> OpenAI Chat
	// SSE -> OpenAI Responses SSE so the client receives proper response.* events including
	// the terminal response.completed.
	isResponses := from == sdktranslator.FormatOpenAIResponse
	requestForResponses := opts.OriginalRequest
	if len(requestForResponses) == 0 {
		requestForResponses = requestBody
	}

	var param any
	var paramChat any
	var paramResp any

	emit := func(chunk []byte) bool {
		select {
		case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
			return true
		case <-ctx.Done():
			return false
		}
	}

	send := func(event []byte) bool {
		helps.AppendAPIResponseChunk(ctx, e.cfg, event)
		var chunks [][]byte
		if isResponses {
			chunks = translateKiroStreamToResponses(ctx, req.Model, opts.OriginalRequest, requestForResponses, event, &paramChat, &paramResp)
		} else {
			chunks = sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, requestBody, event, &param)
		}
		for _, chunk := range chunks {
			if !emit(chunk) {
				return false
			}
		}
		return true
	}

	if !send(kiroclaude.BuildClaudeMessageStartEvent(req.Model, 0)) {
		return
	}
	if !send(kiroclaude.BuildClaudeContentBlockStartEvent(0, "text", "", "")) {
		return
	}

	reader := bufio.NewReaderSize(httpResp.Body, kiroScannerMaxSize)
	var outputTokens int64
	for {
		payload, err := readKiroEventPayload(reader)
		if err != nil {
			if err != io.EOF {
				helps.RecordAPIResponseError(ctx, e.cfg, err)
				reporter.PublishFailure(ctx, err)
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: err}:
				case <-ctx.Done():
				}
			}
			break
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, payload)
		text := extractKiroText(payload)
		if text == "" {
			if detail := extractKiroUsage(payload); detail.TotalTokens > 0 {
				reporter.Publish(ctx, detail)
				outputTokens = detail.OutputTokens
			}
			continue
		}
		outputTokens += int64(len(text) / 4)
		if !send(kiroclaude.BuildClaudeStreamEvent(text, 0)) {
			return
		}
	}
	usageDetail := usage.Detail{OutputTokens: outputTokens, TotalTokens: outputTokens}
	reporter.Publish(ctx, usageDetail)
	_ = send(kiroclaude.BuildClaudeContentBlockStopEvent(0))
	_ = send(kiroclaude.BuildClaudeMessageDeltaEvent("end_turn", usageDetail))
	if !send(kiroclaude.BuildClaudeMessageStopOnlyEvent()) {
		return
	}
	// The OpenAI Responses stream translator emits response.completed only when it sees a
	// [DONE] sentinel. Kiro never produces one, so feed it explicitly to flush the terminal event.
	if isResponses {
		for _, chunk := range openairesponses.ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, req.Model, opts.OriginalRequest, requestForResponses, []byte("[DONE]"), &paramResp) {
			if !emit(chunk) {
				return
			}
		}
	}
}

func kiroCredentials(auth *cliproxyauth.Auth) (accessToken, profileARN string) {
	if auth == nil {
		return "", ""
	}
	if auth.Metadata != nil {
		accessToken = metaStringAny(auth.Metadata, "access_token", "accessToken")
		profileARN = metaStringAny(auth.Metadata, "profile_arn", "profileArn")
	}
	if auth.Attributes != nil {
		if accessToken == "" {
			accessToken = strings.TrimSpace(auth.Attributes["access_token"])
		}
		if profileARN == "" {
			profileARN = strings.TrimSpace(auth.Attributes["profile_arn"])
		}
	}
	return accessToken, profileARN
}

func metaStringAny(meta map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := meta[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func buildKiroRequestPayload(from sdktranslator.Format, model, requestedModel, profileARN string, payload []byte, stream bool, headers http.Header, metadata map[string]any) []byte {
	isAgentic, isChatOnly := kiroModelVariantFlags(requestedModel)
	switch from {
	case sdktranslator.FormatOpenAIResponse:
		openaiBody := openairesponses.ConvertOpenAIResponsesRequestToOpenAIChatCompletions(model, payload, stream)
		body, _ := kiroopenai.BuildKiroPayloadFromOpenAI(openaiBody, model, profileARN, "AI_EDITOR", isAgentic, isChatOnly, headers, metadata)
		return prepareKiroPayload(body, model, profileARN)
	case sdktranslator.FormatOpenAI:
		body, _ := kiroopenai.BuildKiroPayloadFromOpenAI(payload, model, profileARN, "AI_EDITOR", isAgentic, isChatOnly, headers, metadata)
		return prepareKiroPayload(body, model, profileARN)
	case sdktranslator.FormatClaude:
		body, _ := kiroclaude.BuildKiroPayload(payload, model, profileARN, "AI_EDITOR", isAgentic, isChatOnly, headers, metadata)
		return prepareKiroPayload(body, model, profileARN)
	default:
		to := sdktranslator.FromString("kiro")
		body := sdktranslator.TranslateRequest(from, to, model, payload, stream)
		return prepareKiroPayload(body, model, profileARN)
	}
}

func translateKiroNonStreamResponse(ctx context.Context, upstream, client sdktranslator.Format, model string, originalRequest, requestBody, claudeBody []byte) []byte {
	var param any
	if client == sdktranslator.FormatOpenAIResponse {
		openaiBody := sdktranslator.TranslateNonStream(ctx, upstream, sdktranslator.FormatOpenAI, model, originalRequest, requestBody, claudeBody, &param)
		param = nil
		requestForResponses := originalRequest
		if len(requestForResponses) == 0 {
			requestForResponses = requestBody
		}
		return openairesponses.ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(ctx, model, originalRequest, requestForResponses, openaiBody, &param)
	}
	return sdktranslator.TranslateNonStream(ctx, upstream, client, model, originalRequest, requestBody, claudeBody, &param)
}

// translateKiroStreamToResponses chains Kiro's Claude SSE -> OpenAI Chat SSE -> OpenAI Responses SSE.
// paramChat and paramResp hold the independent streaming state for each stage and must persist
// across the whole stream.
func translateKiroStreamToResponses(ctx context.Context, model string, originalRequest, requestForResponses, event []byte, paramChat, paramResp *any) [][]byte {
	chatChunks := kiroopenai.ConvertKiroStreamToOpenAI(ctx, model, originalRequest, requestForResponses, event, paramChat)
	var out [][]byte
	for _, chatChunk := range chatChunks {
		out = append(out, openairesponses.ConvertOpenAIChatCompletionsResponseToOpenAIResponses(ctx, model, originalRequest, requestForResponses, chatChunk, paramResp)...)
	}
	return out
}

func kiroModelVariantFlags(model string) (isAgentic, isChatOnly bool) {
	model = strings.ToLower(strings.TrimSpace(model))
	if idx := strings.LastIndex(model, "/"); idx >= 0 && idx < len(model)-1 {
		model = strings.TrimSpace(model[idx+1:])
	}
	return strings.HasSuffix(model, "-agentic"), strings.HasSuffix(model, "-chat")
}

// finalizeCompactResponse adjusts the OpenAI Responses translation for /responses/compact.
// The compact client expects {object:"response.compaction"} without output items, because
// compaction is a housekeeping operation rather than a user-visible message.
func finalizeCompactResponse(raw []byte) []byte {
	raw, _ = sjson.SetBytes(raw, "object", "response.compaction")
	raw, _ = sjson.DeleteBytes(raw, "output")
	raw, _ = sjson.DeleteBytes(raw, "status")
	raw, _ = sjson.DeleteBytes(raw, "incomplete_details")
	return raw
}

func prepareKiroPayload(body []byte, model, profileARN string) []byte {
	body, _ = sjson.SetBytes(body, "conversationState.currentMessage.userInputMessage.modelId", normalizeKiroModel(model))
	if profileARN != "" {
		body, _ = sjson.SetBytes(body, "profileArn", profileARN)
	} else {
		body, _ = sjson.DeleteBytes(body, "profileArn")
	}
	return body
}

func normalizeKiroModel(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if idx := strings.LastIndex(model, "/"); idx >= 0 && idx < len(model)-1 {
		model = strings.TrimSpace(model[idx+1:])
	}
	model = strings.TrimSuffix(model, "-agentic")
	model = strings.TrimSuffix(model, "-chat")
	replacer := strings.NewReplacer(
		"kiro-", "",
		"amazonq-", "",
		"claude-sonnet-4-5", "claude-sonnet-4.5",
		"claude-opus-4-5", "claude-opus-4.5",
		"claude-haiku-4-5", "claude-haiku-4.5",
		"claude-sonnet-4-6", "claude-sonnet-4.6",
		"claude-opus-4-6", "claude-opus-4.6",
	)
	model = replacer.Replace(model)
	if model == "" || model == "auto" {
		return "auto"
	}
	switch model {
	case "opus", "claude-opus":
		return "claude-opus-4.5"
	case "haiku", "claude-haiku":
		return "claude-haiku-4.5"
	case "sonnet", "claude-sonnet":
		return "claude-sonnet-4.5"
	}
	return model
}

func kiroEndpoints(auth *cliproxyauth.Auth, profileARN string) []kiroEndpoint {
	region := kiroDefaultRegion
	if auth != nil && auth.Metadata != nil {
		if v := metaStringAny(auth.Metadata, "api_region", "region"); v != "" {
			region = v
		}
	}
	if profileARN != "" {
		parts := strings.Split(profileARN, ":")
		if len(parts) > 3 && strings.TrimSpace(parts[3]) != "" {
			region = strings.TrimSpace(parts[3])
		}
	}
	return []kiroEndpoint{
		{
			URL:    fmt.Sprintf("https://q.%s.amazonaws.com/generateAssistantResponse", region),
			Name:   "AmazonQ",
			Origin: "AI_EDITOR",
		},
		{
			URL:    fmt.Sprintf("https://codewhisperer.%s.amazonaws.com/generateAssistantResponse", region),
			Target: "AmazonCodeWhispererStreamingService.GenerateAssistantResponse",
			Name:   "CodeWhisperer",
			Origin: "AI_EDITOR",
		},
	}
}

func applyKiroHeaders(req *http.Request, auth *cliproxyauth.Auth, token string, endpoint kiroEndpoint) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("x-amzn-kiro-agent-mode", kiroAgentMode)
	req.Header.Set("x-amzn-codewhisperer-optout", "true")
	req.Header.Set("Amz-Sdk-Request", "attempt=1; max=3")
	req.Header.Set("Amz-Sdk-Invocation-Id", uuid.NewString())
	req.Header.Set("User-Agent", "aws-sdk-js/1.0.27")
	req.Header.Set("X-Amz-User-Agent", "aws-sdk-js/1.0.27")
	if endpoint.Target != "" {
		req.Header.Set("X-Amz-Target", endpoint.Target)
	}
	if auth != nil {
		util.ApplyCustomHeadersFromAttrs(req, auth.Attributes)
	}
}

func recordKiroRequest(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, url string, headers http.Header, body []byte) {
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   headers.Clone(),
		Body:      body,
		Provider:  kiroProvider,
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
}

func readKiroEventPayload(reader *bufio.Reader) ([]byte, error) {
	prelude, err := reader.Peek(12)
	if err != nil {
		return nil, err
	}
	totalLength := binary.BigEndian.Uint32(prelude[0:4])
	headersLength := binary.BigEndian.Uint32(prelude[4:8])
	if totalLength < 16 || totalLength > 20<<20 || headersLength > totalLength-16 {
		line, errLine := reader.ReadBytes('\n')
		if len(line) > 0 {
			return bytes.TrimSpace(line), nil
		}
		return nil, errLine
	}
	raw := make([]byte, totalLength)
	if _, err := io.ReadFull(reader, raw); err != nil {
		return nil, err
	}
	payloadStart := 12 + int(headersLength)
	payloadEnd := int(totalLength) - 4
	if payloadStart > payloadEnd {
		return nil, fmt.Errorf("kiro event stream: invalid frame")
	}
	return bytes.TrimSpace(raw[payloadStart:payloadEnd]), nil
}

func parseKiroEventStreamPayloads(raw []byte) (string, []kiroclaude.KiroToolUse, usage.Detail, string) {
	reader := bufio.NewReader(bytes.NewReader(raw))
	var content strings.Builder
	var detail usage.Detail
	stopReason := "end_turn"
	processedTools := map[string]bool{}
	var toolUses []kiroclaude.KiroToolUse
	for {
		payload, err := readKiroEventPayload(reader)
		if err != nil {
			if len(raw) > 0 && content.Len() == 0 && gjson.ValidBytes(raw) {
				payload = raw
			} else {
				break
			}
		}
		text := extractKiroText(payload)
		if text != "" {
			remaining, embedded := kiroclaude.ParseEmbeddedToolCalls(text, processedTools)
			content.WriteString(remaining)
			toolUses = append(toolUses, embedded...)
			if detail.OutputTokens == 0 {
				detail.OutputTokens = int64(len(text) / 4)
			}
		}
		if usageEvent := extractKiroUsage(payload); usageEvent.TotalTokens > 0 {
			detail = usageEvent
		}
		if sr := firstGJSON(payload, "stop_reason", "stopReason", "assistantResponseEvent.stopReason", "messageStopEvent.stopReason"); sr != "" {
			stopReason = sr
		}
		if err != nil {
			break
		}
	}
	if detail.TotalTokens == 0 {
		detail.TotalTokens = detail.InputTokens + detail.OutputTokens
	}
	return content.String(), toolUses, detail, stopReason
}

func extractKiroText(payload []byte) string {
	return firstGJSON(payload,
		"content",
		"text",
		"delta",
		"assistantResponseEvent.content",
		"assistantResponseEvent.delta",
		"assistantResponseEvent.text",
		"chunk.bytes",
	)
}

func extractKiroUsage(payload []byte) usage.Detail {
	input := firstGJSONInt(payload, "usage.input_tokens", "usage.inputTokens", "tokenUsage.inputTokens", "messageMetadataEvent.inputTokens", "inputTokens")
	output := firstGJSONInt(payload, "usage.output_tokens", "usage.outputTokens", "tokenUsage.outputTokens", "messageMetadataEvent.outputTokens", "outputTokens")
	total := firstGJSONInt(payload, "usage.total_tokens", "usage.totalTokens", "tokenUsage.totalTokens", "messageMetadataEvent.totalTokens", "totalTokens")
	if total == 0 {
		total = input + output
	}
	return usage.Detail{InputTokens: input, OutputTokens: output, TotalTokens: total}
}

func firstGJSON(payload []byte, paths ...string) string {
	for _, path := range paths {
		if result := gjson.GetBytes(payload, path); result.Exists() {
			if value := strings.TrimSpace(result.String()); value != "" {
				return value
			}
		}
	}
	return ""
}

func firstGJSONInt(payload []byte, paths ...string) int64 {
	for _, path := range paths {
		if result := gjson.GetBytes(payload, path); result.Exists() {
			return result.Int()
		}
	}
	return 0
}
