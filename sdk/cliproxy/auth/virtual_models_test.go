package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/routing"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type virtualChainExecutor struct {
	id string

	mu        sync.Mutex
	models    []string
	failures  map[string]error
	responses map[string][]byte
}

func (e *virtualChainExecutor) Identifier() string { return e.id }

func (e *virtualChainExecutor) Execute(_ context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.models = append(e.models, req.Model)
	err := e.failures[req.Model]
	payload := e.responses[req.Model]
	e.mu.Unlock()
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: payload}, nil
}

func (e *virtualChainExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (e *virtualChainExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *virtualChainExecutor) CountTokens(_ context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.Execute(context.Background(), nil, req, cliproxyexecutor.Options{})
}

func (e *virtualChainExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *virtualChainExecutor) Models() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.models))
	copy(out, e.models)
	return out
}

func TestManagerExecuteVirtualChainFallsThrough(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(0, 0, 0)

	codexExec := &virtualChainExecutor{
		id:       "codex",
		failures: map[string]error{"gpt-bad": &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"}},
	}
	claudeExec := &virtualChainExecutor{
		id:        "claude",
		responses: map[string][]byte{"claude-good": []byte("ok")},
	}
	m.RegisterExecutor(codexExec)
	m.RegisterExecutor(claudeExec)

	registerVirtualTestAuth(t, m, reg, "auth-codex-virtual", "codex", "gpt-bad")
	registerVirtualTestAuth(t, m, reg, "auth-claude-virtual", "claude", "claude-good")

	resp, err := m.Execute(context.Background(), []string{"codex", "claude"}, cliproxyexecutor.Request{Model: "fast"}, cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey: "fast",
			cliproxyexecutor.VirtualTargetsMetadataKey: []routing.VirtualTarget{
				{Provider: "codex", Model: "gpt-bad"},
				{Provider: "claude", Model: "claude-good"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if string(resp.Payload) != "ok" {
		t.Fatalf("payload = %q, want ok", resp.Payload)
	}
	if got := codexExec.Models(); len(got) != 1 || got[0] != "gpt-bad" {
		t.Fatalf("codex models = %#v", got)
	}
	if got := claudeExec.Models(); len(got) != 1 || got[0] != "claude-good" {
		t.Fatalf("claude models = %#v", got)
	}
}

func TestManagerExecuteVirtualChainInvalidRequestIsTerminal(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	m := NewManager(nil, nil, nil)
	m.SetRetryConfig(0, 0, 0)

	codexExec := &virtualChainExecutor{
		id:       "codex",
		failures: map[string]error{"gpt-bad": &Error{HTTPStatus: http.StatusBadRequest, Message: "invalid_request_error: bad input"}},
	}
	claudeExec := &virtualChainExecutor{
		id:        "claude",
		responses: map[string][]byte{"claude-good": []byte("ok")},
	}
	m.RegisterExecutor(codexExec)
	m.RegisterExecutor(claudeExec)

	registerVirtualTestAuth(t, m, reg, "auth-codex-invalid", "codex", "gpt-bad")
	registerVirtualTestAuth(t, m, reg, "auth-claude-invalid", "claude", "claude-good")

	_, err := m.Execute(context.Background(), []string{"codex", "claude"}, cliproxyexecutor.Request{Model: "fast"}, cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestedModelMetadataKey: "fast",
			cliproxyexecutor.VirtualTargetsMetadataKey: []routing.VirtualTarget{
				{Provider: "codex", Model: "gpt-bad"},
				{Provider: "claude", Model: "claude-good"},
			},
		},
	})
	if err == nil {
		t.Fatal("Execute() error = nil, want invalid request")
	}
	if got := claudeExec.Models(); len(got) != 0 {
		t.Fatalf("claude models = %#v, want no fallback call", got)
	}
}

func registerVirtualTestAuth(t *testing.T, m *Manager, reg *registry.ModelRegistry, authID, provider, model string) {
	t.Helper()
	if _, err := m.Register(context.Background(), &Auth{ID: authID, Provider: provider}); err != nil {
		t.Fatalf("register auth %s: %v", authID, err)
	}
	reg.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
	m.RefreshSchedulerEntry(authID)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})
}
