package server

import (
	"errors"
	"net/http"

	sentinel "sentinel-go"
)

func errorResponseFromError(err error) (int, ErrorResponse) {
	status := http.StatusInternalServerError
	detail := ErrorDetail{Message: err.Error(), Type: "server_error"}

	var ue *sentinel.UpstreamError
	if errors.As(err, &ue) {
		status = httpStatusForUpstreamError(ue)
		detail.Type = errorTypeForCode(ue.Code)
		detail.Code = string(ue.Code)
		if ue.Message != "" {
			detail.Message = ue.Error()
		}
	}
	return status, ErrorResponse{Error: detail}
}

func httpStatusForUpstreamError(err *sentinel.UpstreamError) int {
	if err == nil {
		return http.StatusInternalServerError
	}
	switch err.Code {
	case sentinel.ErrAuthInvalidAccessToken, sentinel.ErrAuthSessionRefreshFailed, sentinel.ErrAuthCookieInvalid:
		return http.StatusUnauthorized
	case sentinel.ErrUpstreamRateLimited:
		return http.StatusTooManyRequests
	case sentinel.ErrUpstreamForbidden, sentinel.ErrUpstreamFingerprintRejected, sentinel.ErrSentinelTurnstileRequired, sentinel.ErrCSRFRequired:
		return http.StatusForbidden
	case sentinel.ErrConversationExpired:
		return http.StatusConflict
	}
	if err.StatusCode >= 400 && err.StatusCode < 600 {
		return err.StatusCode
	}
	return http.StatusInternalServerError
}

func errorTypeForCode(code sentinel.ErrorCode) string {
	switch code {
	case sentinel.ErrAuthInvalidAccessToken, sentinel.ErrAuthSessionRefreshFailed, sentinel.ErrAuthCookieInvalid:
		return "authentication_error"
	case sentinel.ErrUpstreamRateLimited:
		return "rate_limit_error"
	case sentinel.ErrConversationExpired:
		return "invalid_request_error"
	case sentinel.ErrCSRFRequired, sentinel.ErrSentinelTurnstileRequired, sentinel.ErrUpstreamForbidden, sentinel.ErrUpstreamFingerprintRejected:
		return "permission_error"
	default:
		return "server_error"
	}
}
