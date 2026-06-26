package sentinel

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrorCode is a stable, machine-readable category for upstream failures.
type ErrorCode string

const (
	ErrAuthInvalidAccessToken      ErrorCode = "auth_invalid_access_token"
	ErrAuthSessionRefreshFailed    ErrorCode = "auth_session_refresh_failed"
	ErrAuthCookieInvalid           ErrorCode = "auth_cookie_invalid"
	ErrCSRFRequired                ErrorCode = "csrf_required"
	ErrSentinelPrepareFailed       ErrorCode = "sentinel_prepare_failed"
	ErrSentinelFinalizeFailed      ErrorCode = "sentinel_finalize_failed"
	ErrSentinelTurnstileRequired   ErrorCode = "sentinel_turnstile_required"
	ErrSentinelPoWFailed           ErrorCode = "sentinel_pow_failed"
	ErrUpstreamRateLimited         ErrorCode = "upstream_rate_limited"
	ErrUpstreamForbidden           ErrorCode = "upstream_forbidden"
	ErrUpstreamFingerprintRejected ErrorCode = "upstream_fingerprint_rejected"
	ErrStreamHandoffFailed         ErrorCode = "stream_handoff_failed"
	ErrWSDisconnected              ErrorCode = "ws_disconnected"
	ErrConversationExpired         ErrorCode = "conversation_expired"
	ErrFrontendSchemaChanged       ErrorCode = "frontend_schema_changed"
	ErrUpstreamHTTP                ErrorCode = "upstream_http_error"
)

// UpstreamError preserves status/code/retry metadata while still behaving like a Go error.
type UpstreamError struct {
	Code       ErrorCode
	Message    string
	StatusCode int
	Body       string
	RetryAfter time.Duration
	Retryable  bool
	Cause      error
}

func (e *UpstreamError) Error() string {
	if e == nil {
		return ""
	}
	msg := e.Message
	if msg == "" {
		msg = string(e.Code)
	}
	if e.StatusCode > 0 {
		msg = fmt.Sprintf("%s: HTTP %d", msg, e.StatusCode)
	}
	if e.Body != "" {
		msg += ": " + truncateStr(e.Body, 300)
	}
	if e.Cause != nil {
		msg += ": " + e.Cause.Error()
	}
	return msg
}

func (e *UpstreamError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func NewUpstreamError(code ErrorCode, message string, status int, body string, cause error) *UpstreamError {
	ue := &UpstreamError{Code: code, Message: message, StatusCode: status, Body: truncateStr(body, 500), Cause: cause}
	ue.Retryable = isRetryableCode(code) || status == http.StatusTooManyRequests || status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
	if status == http.StatusForbidden {
		ue.Retryable = false
	}
	return ue
}

func ClassifyHTTPError(context string, status int, body string, retryAfterHeader string) *UpstreamError {
	lower := strings.ToLower(body)
	code := ErrUpstreamHTTP
	msg := context
	switch {
	case status == http.StatusUnauthorized:
		code, msg = ErrAuthInvalidAccessToken, context+" unauthorized"
	case status == http.StatusTooManyRequests:
		code, msg = ErrUpstreamRateLimited, context+" rate limited"
	case status == http.StatusForbidden:
		code, msg = classifyForbiddenBody(lower), context+" forbidden"
	case strings.Contains(lower, "csrf") || strings.Contains(lower, "xsrf"):
		code, msg = ErrCSRFRequired, context+" csrf required"
	case strings.Contains(lower, "conversation") && (strings.Contains(lower, "expired") || strings.Contains(lower, "not found") || strings.Contains(lower, "invalid")):
		code, msg = ErrConversationExpired, context+" conversation expired"
	}
	ue := NewUpstreamError(code, msg, status, body, nil)
	ue.RetryAfter = parseRetryAfter(retryAfterHeader)
	if ue.RetryAfter > 0 && status == http.StatusTooManyRequests {
		ue.Retryable = true
	}
	return ue
}

func classifyForbiddenBody(lower string) ErrorCode {
	switch {
	case strings.Contains(lower, "csrf") || strings.Contains(lower, "xsrf"):
		return ErrCSRFRequired
	case strings.Contains(lower, "cookie") || strings.Contains(lower, "session"):
		return ErrAuthCookieInvalid
	case strings.Contains(lower, "sentinel") || strings.Contains(lower, "proof") || strings.Contains(lower, "pow"):
		return ErrSentinelFinalizeFailed
	case strings.Contains(lower, "fingerprint") || strings.Contains(lower, "unsupported browser"):
		return ErrUpstreamFingerprintRejected
	default:
		return ErrUpstreamForbidden
	}
}

func isRetryableCode(code ErrorCode) bool {
	switch code {
	case ErrUpstreamRateLimited:
		return true
	default:
		return false
	}
}

func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if sec, err := strconv.Atoi(v); err == nil && sec > 0 {
		return time.Duration(sec) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

func IsErrorCode(err error, code ErrorCode) bool {
	var ue *UpstreamError
	if errors.As(err, &ue) {
		return ue.Code == code
	}
	return false
}

func ErrorInfo(err error) (ErrorCode, int, bool) {
	var ue *UpstreamError
	if errors.As(err, &ue) {
		return ue.Code, ue.StatusCode, ue.Retryable
	}
	return "", 0, false
}
