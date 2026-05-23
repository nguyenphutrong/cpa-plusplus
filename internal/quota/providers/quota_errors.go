package providers

import "errors"

var ErrQuotaNotImplemented = errors.New("not implemented")
var ErrUnauthorized = errors.New("unauthorized")
var ErrRateLimited = errors.New("rate limited")
