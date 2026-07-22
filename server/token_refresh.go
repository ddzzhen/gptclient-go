package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/imroc/req/v3"
)

const (
	sessionAuthURL             = "https://chatgpt.com/api/auth/session"
	tlsFingerprintChromeVersion = "150"
)

var defaultRefreshUA = fmt.Sprintf("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%s.0.0.0 Safari/537.36", tlsFingerprintChromeVersion)

func RefreshATFromSession(sessionToken string) (accessToken string, expiresAt time.Time, err error) {
	sessionToken = normalizeSessionToken(sessionToken)
	if sessionToken == "" {
		return "", time.Time{}, errors.New("session token is empty")
	}

	c := req.C().
		SetBaseURL("https://chatgpt.com").
		ImpersonateChrome()

	cookieVal := "__Secure-next-auth.session-token=" + sessionToken

	resp, err := c.R().
		SetHeaders(map[string]string{
			"Accept":                "application/json",
			"Referer":              "https://chatgpt.com/",
			"Origin":               "https://chatgpt.com",
			"User-Agent":           defaultRefreshUA,
			"Accept-Language":      "zh-CN,zh;q=0.9,en;q=0.8,en-GB;q=0.7,en-US;q=0.6",
			"Cookie":               cookieVal,
			"sec-ch-ua":            fmt.Sprintf(`"Chromium";v="%s", "Not-A.Brand";v="24", "Google Chrome";v="%s"`, tlsFingerprintChromeVersion, tlsFingerprintChromeVersion),
			"sec-ch-ua-mobile":     "?0",
			"sec-ch-ua-platform":   `"Windows"`,
			"sec-ch-ua-platform-version": `"19.0.0"`,
			"sec-ch-ua-arch":              `"x86"`,
			"sec-ch-ua-bitness":           `"64"`,
			"sec-ch-ua-model":             `""`,
			"sec-ch-ua-full-version":      fmt.Sprintf(`"%s.0.0.0"`, tlsFingerprintChromeVersion),
			"sec-ch-ua-full-version-list": fmt.Sprintf(`"Chromium";v="%s.0.0.0", "Not-A.Brand";v="24.0.0.0", "Google Chrome";v="%s.0.0.0"`, tlsFingerprintChromeVersion, tlsFingerprintChromeVersion),
			"sec-fetch-dest":              "empty",
			"sec-fetch-mode":              "cors",
			"sec-fetch-site":              "same-origin",
		}).
		Get("/api/auth/session")
	if err != nil {
		return "", time.Time{}, fmt.Errorf("session refresh request: %w", err)
	}

	data := resp.Bytes()
	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("session refresh http=%d body=%s", resp.StatusCode, truncateBody(string(data), 200))
	}

	raw := strings.TrimSpace(string(data))
	if raw == "" || raw == "{}" {
		return "", time.Time{}, errors.New("session token expired or invalid (empty response)")
	}

	var out struct {
		AccessToken string `json:"accessToken"`
		Expires     string `json:"expires"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return "", time.Time{}, fmt.Errorf("parse session response: %w", err)
	}
	if out.AccessToken == "" {
		return "", time.Time{}, errors.New("session response missing accessToken")
	}

	expiresAt = time.Time{}
	if out.Expires != "" {
		if t, e := time.Parse(time.RFC3339, out.Expires); e == nil {
			expiresAt = t
		}
	}
	if expiresAt.IsZero() {
		expiresAt = parseJWTExp(out.AccessToken)
	}
	return out.AccessToken, expiresAt, nil
}

func RefreshATFromBrowser(ctx context.Context, bm interface {
	RefreshAccessToken(ctx context.Context) (string, time.Time, error)
}, sessionToken string) (accessToken string, expiresAt time.Time, err error) {
	if bm == nil {
		return RefreshATFromSession(sessionToken)
	}
	return bm.RefreshAccessToken(ctx)
}

func normalizeSessionToken(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "st:")
	s = strings.TrimPrefix(s, "session:")
	s = strings.TrimPrefix(s, "Bearer ")
	s = strings.TrimPrefix(s, "bearer ")
	lower := strings.ToLower(s)
	if strings.HasPrefix(lower, "__secure-next-auth.session-token") {
		if i := strings.Index(s, "="); i >= 0 && i < len(s)-1 {
			s = s[i+1:]
		}
	}
	return strings.TrimSpace(s)
}

func parseJWTExp(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Now().Add(24 * time.Hour)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		raw, err = base64.StdEncoding.DecodeString(parts[1])
		if err != nil {
			return time.Now().Add(24 * time.Hour)
		}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil || claims.Exp == 0 {
		return time.Now().Add(24 * time.Hour)
	}
	return time.Unix(claims.Exp, 0)
}

func isAccessToken(s string) bool {
	if !strings.HasPrefix(s, "eyJ") || strings.Count(s, ".") < 2 {
		return false
	}
	if strings.HasPrefix(s, "eyJhbGciOiJkaXIi") {
		return false
	}
	return true
}

func isLikelySessionToken(s string) bool {
	if isAccessToken(s) || strings.Contains(s, "{") || strings.Contains(s, " ") {
		return false
	}
	return len(s) >= 40
}

func truncateBody(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
