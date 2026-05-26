package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	copilotauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	githubCopilotProvider      = "github-copilot"
	githubCopilotChatPath      = "/chat/completions"
	githubCopilotResponsesPath = "/responses"
	githubCopilotTokenTTL      = 25 * time.Minute
	githubCopilotExpiryBuffer  = 5 * time.Minute
	githubCopilotScannerLimit  = 20_971_520

	githubCopilotUserAgent     = "GitHubCopilotChat/0.35.0"
	githubCopilotEditorVersion = "vscode/1.107.0"
	githubCopilotPluginVersion = "copilot-chat/0.35.0"
	githubCopilotIntegrationID = "vscode-chat"
	githubCopilotOpenAIIntent  = "conversation-panel"
	githubCopilotGitHubAPIVer  = "2025-04-01"
)

type GitHubCopilotExecutor struct {
	cfg   *config.Config
	mu    sync.RWMutex
	cache map[string]*githubCopilotCachedToken
}

type githubCopilotCachedToken struct {
	token       string
	apiEndpoint string
	expiresAt   time.Time
}

func NewGitHubCopilotExecutor(cfg *config.Config) *GitHubCopilotExecutor {
	return &GitHubCopilotExecutor{
		cfg:   cfg,
		cache: make(map[string]*githubCopilotCachedToken),
	}
}

func (e *GitHubCopilotExecutor) Identifier() string { return githubCopilotProvider }

func (e *GitHubCopilotExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	token, _, err := e.ensureAPIToken(req.Context(), auth)
	if err != nil {
		return err
	}
	e.applyHeaders(req, token, nil)
	return nil
}

func (e *GitHubCopilotExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("github-copilot executor: request is nil")
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

func (e *GitHubCopilotExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	token, baseURL, errToken := e.ensureAPIToken(ctx, auth)
	if errToken != nil {
		err = errToken
		return resp, err
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	body, originalTranslated, to, endpoint, errPrepare := e.preparePayload(req, opts, false)
	if errPrepare != nil {
		err = errPrepare
		return resp, err
	}
	body, _ = sjson.SetBytes(body, "stream", false)

	url := strings.TrimRight(baseURL, "/") + endpoint
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}
	e.applyHeaders(httpReq, token, body)
	if detectGitHubCopilotVisionContent(body) {
		httpReq.Header.Set("Copilot-Vision-Request", "true")
	}
	e.recordRequest(ctx, auth, url, httpReq.Header, body)

	httpResp, err := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("github-copilot executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = statusErr{code: httpResp.StatusCode, msg: string(data)}
		return resp, err
	}

	reporter.Publish(ctx, helps.ParseOpenAIUsage(data))
	reporter.EnsurePublished(ctx)

	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, opts.SourceFormat, req.Model, opts.OriginalRequest, originalTranslated, data, &param)
	return cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}, nil
}

func (e *GitHubCopilotExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	token, baseURL, errToken := e.ensureAPIToken(ctx, auth)
	if errToken != nil {
		err = errToken
		return nil, err
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName
	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	body, _, to, endpoint, errPrepare := e.preparePayload(req, opts, true)
	if errPrepare != nil {
		err = errPrepare
		return nil, err
	}
	body, _ = sjson.SetBytes(body, "stream", true)
	if endpoint == githubCopilotChatPath {
		body, _ = sjson.SetBytes(body, "stream_options.include_usage", true)
	}

	url := strings.TrimRight(baseURL, "/") + endpoint
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	e.applyHeaders(httpReq, token, body)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")
	if detectGitHubCopilotVisionContent(body) {
		httpReq.Header.Set("Copilot-Vision-Request", "true")
	}
	e.recordRequest(ctx, auth, url, httpReq.Header, body)

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
			log.Errorf("github-copilot executor: close response body error: %v", errClose)
		}
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = statusErr{code: httpResp.StatusCode, msg: string(data)}
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("github-copilot executor: close response body error: %v", errClose)
			}
			reporter.EnsurePublished(ctx)
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, githubCopilotScannerLimit)
		var param any
		for scanner.Scan() {
			line := bytes.Clone(scanner.Bytes())
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			if detail, ok := helps.ParseOpenAIStreamUsage(line); ok {
				reporter.Publish(ctx, detail)
			}
			for _, chunk := range sdktranslator.TranslateStream(ctx, to, opts.SourceFormat, req.Model, opts.OriginalRequest, body, line, &param) {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
				case <-ctx.Done():
					return
				}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx, errScan)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
			return
		}
		for _, chunk := range sdktranslator.TranslateStream(ctx, to, opts.SourceFormat, req.Model, opts.OriginalRequest, body, []byte("data: [DONE]"), &param) {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
			case <-ctx.Done():
				return
			}
		}
	}()

	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *GitHubCopilotExecutor) CountTokens(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, statusErr{code: http.StatusNotImplemented, msg: "count tokens not supported for github-copilot"}
}

func (e *GitHubCopilotExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, statusErr{code: http.StatusUnauthorized, msg: "missing auth"}
	}
	accessToken := githubCopilotAccessToken(auth)
	if accessToken == "" {
		return auth, nil
	}
	if _, err := copilotauth.NewAuth(e.cfg, nil).GetAPIToken(ctx, accessToken); err != nil {
		return nil, statusErr{code: http.StatusUnauthorized, msg: fmt.Sprintf("github-copilot token validation failed: %v", err)}
	}
	return auth, nil
}

func (e *GitHubCopilotExecutor) preparePayload(req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) ([]byte, []byte, sdktranslator.Format, string, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	endpoint := githubCopilotChatPath
	if useGitHubCopilotResponsesEndpoint(from, req.Model) {
		to = sdktranslator.FromString("openai-response")
		endpoint = githubCopilotResponsesPath
	}

	originalPayload := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayload = opts.OriginalRequest
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, stream)
	body := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, stream)
	body = normalizeGitHubCopilotModel(req.Model, body)
	body = flattenGitHubCopilotAssistantContent(body)

	thinkingProvider := "openai"
	if endpoint == githubCopilotResponsesPath {
		thinkingProvider = "codex"
	}
	var err error
	body, err = thinking.ApplyThinking(body, req.Model, from.String(), thinkingProvider, e.Identifier())
	if err != nil {
		return nil, nil, "", "", err
	}

	if endpoint == githubCopilotResponsesPath {
		body = normalizeGitHubCopilotResponsesInput(body)
		body = normalizeGitHubCopilotResponsesTools(body)
	} else {
		body = normalizeGitHubCopilotChatTools(body)
	}
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	return body, originalTranslated, to, endpoint, nil
}

func (e *GitHubCopilotExecutor) ensureAPIToken(ctx context.Context, auth *cliproxyauth.Auth) (string, string, error) {
	if auth == nil {
		return "", "", statusErr{code: http.StatusUnauthorized, msg: "missing auth"}
	}
	accessToken := githubCopilotAccessToken(auth)
	if accessToken == "" {
		return "", "", statusErr{code: http.StatusUnauthorized, msg: "missing github access token"}
	}

	e.mu.RLock()
	if cached, ok := e.cache[accessToken]; ok && cached != nil && cached.expiresAt.After(time.Now().Add(githubCopilotExpiryBuffer)) {
		e.mu.RUnlock()
		return cached.token, cached.apiEndpoint, nil
	}
	e.mu.RUnlock()

	apiToken, err := copilotauth.NewAuth(e.cfg, nil).GetAPIToken(ctx, accessToken)
	if err != nil {
		return "", "", statusErr{code: http.StatusUnauthorized, msg: fmt.Sprintf("failed to get copilot api token: %v", err)}
	}
	apiEndpoint := strings.TrimRight(copilotauth.BaseURL, "/")
	if apiToken.Endpoints.API != "" {
		apiEndpoint = strings.TrimRight(apiToken.Endpoints.API, "/")
	}
	if baseURL := githubCopilotAuthBaseURL(auth); baseURL != "" {
		apiEndpoint = strings.TrimRight(baseURL, "/")
	}
	expiresAt := time.Now().Add(githubCopilotTokenTTL)
	if apiToken.ExpiresAt > 0 {
		expiresAt = time.Unix(apiToken.ExpiresAt, 0)
	}
	e.mu.Lock()
	e.cache[accessToken] = &githubCopilotCachedToken{token: apiToken.Token, apiEndpoint: apiEndpoint, expiresAt: expiresAt}
	e.mu.Unlock()
	return apiToken.Token, apiEndpoint, nil
}

func (e *GitHubCopilotExecutor) applyHeaders(req *http.Request, apiToken string, body []byte) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", githubCopilotUserAgent)
	req.Header.Set("Editor-Version", githubCopilotEditorVersion)
	req.Header.Set("Editor-Plugin-Version", githubCopilotPluginVersion)
	req.Header.Set("Openai-Intent", githubCopilotOpenAIIntent)
	req.Header.Set("Copilot-Integration-Id", githubCopilotIntegrationID)
	req.Header.Set("X-Github-Api-Version", githubCopilotGitHubAPIVer)
	req.Header.Set("X-Request-Id", uuid.NewString())
	initiator := "user"
	if role := detectGitHubCopilotLastRole(body); role == "assistant" || role == "tool" {
		initiator = "agent"
	}
	req.Header.Set("X-Initiator", initiator)
}

func (e *GitHubCopilotExecutor) recordRequest(ctx context.Context, auth *cliproxyauth.Auth, url string, headers http.Header, body []byte) {
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   headers.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
}

func githubCopilotAccessToken(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["access_token"].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	if auth.Attributes != nil {
		for _, key := range []string{"access_token", "github_access_token", "api_key"} {
			if v := strings.TrimSpace(auth.Attributes[key]); v != "" {
				return v
			}
		}
	}
	return ""
}

func githubCopilotAuthBaseURL(auth *cliproxyauth.Auth) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	for _, key := range []string{"base_url", "api_base_url", "api_endpoint"} {
		if v := strings.TrimSpace(auth.Attributes[key]); v != "" {
			return v
		}
	}
	return ""
}

func useGitHubCopilotResponsesEndpoint(sourceFormat sdktranslator.Format, model string) bool {
	if sourceFormat.String() == "openai-response" {
		return true
	}
	return strings.Contains(strings.ToLower(thinking.ParseSuffix(model).ModelName), "codex")
}

func normalizeGitHubCopilotModel(model string, body []byte) []byte {
	baseModel := strings.ToLower(thinking.ParseSuffix(model).ModelName)
	if baseModel != "" {
		body, _ = sjson.SetBytes(body, "model", baseModel)
	}
	return body
}

func detectGitHubCopilotLastRole(body []byte) string {
	for _, path := range []string{"messages", "input"} {
		values := gjson.GetBytes(body, path)
		if !values.Exists() || !values.IsArray() {
			continue
		}
		arr := values.Array()
		for i := len(arr) - 1; i >= 0; i-- {
			if role := arr[i].Get("role").String(); role != "" {
				return role
			}
			switch arr[i].Get("type").String() {
			case "function_call", "computer_call":
				return "assistant"
			case "function_call_output", "computer_call_output", "tool_result":
				return "tool"
			}
		}
	}
	return ""
}

func detectGitHubCopilotVisionContent(body []byte) bool {
	for _, message := range gjson.GetBytes(body, "messages").Array() {
		content := message.Get("content")
		if !content.IsArray() {
			continue
		}
		for _, block := range content.Array() {
			if t := block.Get("type").String(); t == "image_url" || t == "image" {
				return true
			}
		}
	}
	return false
}

func flattenGitHubCopilotAssistantContent(body []byte) []byte {
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return body
	}
	result := body
	for i, msg := range messages.Array() {
		if msg.Get("role").String() != "assistant" {
			continue
		}
		content := msg.Get("content")
		if !content.Exists() || !content.IsArray() {
			continue
		}
		var parts []string
		for _, part := range content.Array() {
			t := part.Get("type").String()
			if t != "" && t != "text" {
				parts = nil
				break
			}
			if text := part.Get("text").String(); text != "" {
				parts = append(parts, text)
			}
		}
		if len(parts) == 0 {
			continue
		}
		result, _ = sjson.SetBytes(result, fmt.Sprintf("messages.%d.content", i), strings.Join(parts, ""))
	}
	return result
}

func normalizeGitHubCopilotChatTools(body []byte) []byte {
	tools := gjson.GetBytes(body, "tools")
	if tools.Exists() && tools.IsArray() {
		filtered := "[]"
		for _, tool := range tools.Array() {
			if tool.Get("type").String() == "function" {
				filtered, _ = sjson.SetRaw(filtered, "-1", tool.Raw)
			}
		}
		body, _ = sjson.SetRawBytes(body, "tools", []byte(filtered))
	}
	if choice := gjson.GetBytes(body, "tool_choice"); choice.Exists() && choice.Type != gjson.String {
		body, _ = sjson.SetBytes(body, "tool_choice", "auto")
	}
	return body
}

func normalizeGitHubCopilotResponsesInput(body []byte) []byte {
	body, _ = sjson.DeleteBytes(body, "service_tier")
	if input := gjson.GetBytes(body, "input"); input.Exists() {
		return body
	}
	if messages := gjson.GetBytes(body, "messages"); messages.Exists() {
		body, _ = sjson.SetRawBytes(body, "input", []byte(messages.Raw))
		body, _ = sjson.DeleteBytes(body, "messages")
	}
	body, _ = sjson.DeleteBytes(body, "system")
	return body
}

func normalizeGitHubCopilotResponsesTools(body []byte) []byte {
	tools := gjson.GetBytes(body, "tools")
	if tools.Exists() && tools.IsArray() {
		filtered := "[]"
		for _, tool := range tools.Array() {
			toolType := tool.Get("type").String()
			if toolType == "web_search_preview" || toolType == "computer_use_preview" {
				filtered, _ = sjson.SetRaw(filtered, "-1", tool.Raw)
				continue
			}
			if toolType != "" && toolType != "function" {
				continue
			}
			name := tool.Get("name").String()
			if name == "" {
				name = tool.Get("function.name").String()
			}
			if name == "" {
				continue
			}
			normalized := `{"type":"function","name":""}`
			normalized, _ = sjson.Set(normalized, "name", name)
			if desc := tool.Get("description").String(); desc != "" {
				normalized, _ = sjson.Set(normalized, "description", desc)
			} else if desc := tool.Get("function.description").String(); desc != "" {
				normalized, _ = sjson.Set(normalized, "description", desc)
			}
			if params := tool.Get("parameters"); params.Exists() {
				normalized, _ = sjson.SetRaw(normalized, "parameters", params.Raw)
			} else if params := tool.Get("function.parameters"); params.Exists() {
				normalized, _ = sjson.SetRaw(normalized, "parameters", params.Raw)
			}
			filtered, _ = sjson.SetRaw(filtered, "-1", normalized)
		}
		body, _ = sjson.SetRawBytes(body, "tools", []byte(filtered))
	}
	if choice := gjson.GetBytes(body, "tool_choice"); choice.Exists() && choice.Type != gjson.String {
		body, _ = sjson.SetBytes(body, "tool_choice", "auto")
	}
	return body
}
