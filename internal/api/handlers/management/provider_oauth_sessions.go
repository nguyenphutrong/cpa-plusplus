package management

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type providerOAuthSessionStatus string

const (
	providerOAuthSessionStarting                   providerOAuthSessionStatus = "starting"
	providerOAuthSessionAwaitingCallback           providerOAuthSessionStatus = "awaiting_callback"
	providerOAuthSessionAwaitingDeviceConfirmation providerOAuthSessionStatus = "awaiting_device_confirmation"
	providerOAuthSessionCompleted                  providerOAuthSessionStatus = "completed"
	providerOAuthSessionFailed                     providerOAuthSessionStatus = "failed"
	providerOAuthSessionExpired                    providerOAuthSessionStatus = "expired"
	providerOAuthSessionCancelled                  providerOAuthSessionStatus = "cancelled"
)

type providerOAuthSession struct {
	ID              string
	State           string
	Provider        string
	Method          string
	Status          providerOAuthSessionStatus
	AuthURL         string
	VerificationURI string
	UserCode        string
	ExpiresAt       time.Time
	IntervalSeconds int
	Error           string
	Credential      gin.H
}

type providerOAuthSessionResponse struct {
	SessionID       string                     `json:"session_id"`
	Provider        string                     `json:"provider"`
	Status          providerOAuthSessionStatus `json:"status"`
	AuthURL         string                     `json:"auth_url,omitempty"`
	VerificationURI string                     `json:"verification_uri,omitempty"`
	UserCode        string                     `json:"user_code,omitempty"`
	ExpiresAt       string                     `json:"expires_at,omitempty"`
	IntervalSeconds int                        `json:"interval_seconds,omitempty"`
	Error           string                     `json:"error,omitempty"`
	Credential      gin.H                      `json:"credential,omitempty"`
	State           string                     `json:"state,omitempty"`
}

type providerOAuthSessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*providerOAuthSession
	now      func() time.Time
}

var providerOAuthSessions = newProviderOAuthSessionStore()

func newProviderOAuthSessionStore() *providerOAuthSessionStore {
	return &providerOAuthSessionStore{
		sessions: make(map[string]*providerOAuthSession),
		now:      time.Now,
	}
}

func newProviderOAuthSessionID(provider string) string {
	var raw [9]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return strings.TrimSpace(provider) + "-" + hex.EncodeToString([]byte(time.Now().UTC().Format("20060102150405.000000000")))
	}
	return strings.TrimSpace(provider) + "-" + hex.EncodeToString(raw[:])
}

func storeProviderOAuthSession(session *providerOAuthSession) {
	providerOAuthSessions.Store(session)
}

func completeProviderOAuthSession(sessionID string, credential gin.H) {
	providerOAuthSessions.Complete(sessionID, credential)
}

func failProviderOAuthSession(sessionID, message string) {
	providerOAuthSessions.Fail(sessionID, message)
}

func (s *providerOAuthSessionStore) Store(session *providerOAuthSession) {
	if session == nil || strings.TrimSpace(session.ID) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *session
	s.sessions[session.ID] = &cp
}

func (s *providerOAuthSessionStore) Status(sessionID string) (providerOAuthSessionResponse, bool) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return providerOAuthSessionResponse{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[sessionID]
	if !ok || session == nil {
		return providerOAuthSessionResponse{}, false
	}
	if s.isExpiredLocked(session) {
		session.Status = providerOAuthSessionExpired
		session.Error = "OAuth session expired"
	}
	return providerOAuthSessionToResponse(session), true
}

func (s *providerOAuthSessionStore) Complete(sessionID string, credential gin.H) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.sessions[strings.TrimSpace(sessionID)]
	if session == nil {
		return
	}
	session.Status = providerOAuthSessionCompleted
	session.Error = ""
	session.Credential = credential
}

func (s *providerOAuthSessionStore) Fail(sessionID, message string) {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "Authentication failed"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.sessions[strings.TrimSpace(sessionID)]
	if session == nil {
		return
	}
	if s.isExpiredLocked(session) {
		session.Status = providerOAuthSessionExpired
		session.Error = "OAuth session expired"
		return
	}
	session.Status = providerOAuthSessionFailed
	session.Error = message
}

func (s *providerOAuthSessionStore) Cancel(sessionID string) (providerOAuthSessionResponse, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session := s.sessions[strings.TrimSpace(sessionID)]
	if session == nil {
		return providerOAuthSessionResponse{}, false
	}
	if session.Status != providerOAuthSessionCompleted && session.Status != providerOAuthSessionFailed && session.Status != providerOAuthSessionExpired {
		session.Status = providerOAuthSessionCancelled
		session.Error = "OAuth session cancelled"
		if session.State != "" {
			CompleteOAuthSession(session.State)
		}
	}
	return providerOAuthSessionToResponse(session), true
}

func (s *providerOAuthSessionStore) isExpiredLocked(session *providerOAuthSession) bool {
	if session == nil || session.ExpiresAt.IsZero() {
		return false
	}
	if session.Status == providerOAuthSessionCompleted || session.Status == providerOAuthSessionFailed || session.Status == providerOAuthSessionCancelled {
		return false
	}
	return !session.ExpiresAt.After(s.now())
}

func providerOAuthSessionToResponse(session *providerOAuthSession) providerOAuthSessionResponse {
	if session == nil {
		return providerOAuthSessionResponse{}
	}
	out := providerOAuthSessionResponse{
		SessionID:       session.ID,
		Provider:        session.Provider,
		Status:          session.Status,
		AuthURL:         session.AuthURL,
		VerificationURI: session.VerificationURI,
		UserCode:        session.UserCode,
		IntervalSeconds: session.IntervalSeconds,
		Error:           session.Error,
		Credential:      session.Credential,
		State:           session.State,
	}
	if !session.ExpiresAt.IsZero() {
		out.ExpiresAt = session.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return out
}
