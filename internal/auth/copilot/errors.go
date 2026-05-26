package copilot

import "fmt"

type AuthenticationError struct {
	Type string
	Err  error
}

func (e *AuthenticationError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return e.Type
	}
	return fmt.Sprintf("%s: %v", e.Type, e.Err)
}

func (e *AuthenticationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

const (
	ErrDeviceCodeFailed     = "device_code_failed"
	ErrAuthorizationPending = "authorization_pending"
	ErrSlowDown             = "slow_down"
	ErrDeviceCodeExpired    = "expired_token"
	ErrAccessDenied         = "access_denied"
	ErrTokenExchangeFailed  = "token_exchange_failed"
	ErrPollingTimeout       = "polling_timeout"
)

func newAuthError(kind string, err error) error {
	return &AuthenticationError{Type: kind, Err: err}
}

func UserFriendlyMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
