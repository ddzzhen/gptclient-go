//go:build browser

package sentinel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/storage"
	"github.com/chromedp/chromedp"
)

type BrowserConfig struct {
	Headless       bool
	ChromePath     string
	UserDataDir    string
	RemoteDebugURL string
	Timeout        time.Duration
	CookieString   string
	SessionToken   string
}

type BrowserSession struct {
	AccessToken  string
	CookieString string
	DeviceID     string
	BuildHash    string
	BuildNumber  string
	UserAgent    string
	DPL          string
	ScreenWidth  int
	ScreenHeight int
	PixelRatio   float64
}

type BrowserManager struct {
	cfg          BrowserConfig
	allocCtx     context.Context
	allocCancel  context.CancelFunc
	browserCtx   context.Context
	browserCancel context.CancelFunc
	mu           sync.Mutex
	session      *BrowserSession
	ready        bool
	Logf         LogFunc
}

func NewBrowserManager(cfg BrowserConfig) *BrowserManager {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}
	return &BrowserManager{
		cfg:   cfg,
		Logf:  log.Printf,
		ready: false,
	}
}

func (bm *BrowserManager) IsReady() bool {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	return bm.ready
}

func (bm *BrowserManager) GetSession() *BrowserSession {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	return bm.session
}

func (bm *BrowserManager) Launch(ctx context.Context) error {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	if bm.ready {
		return nil
	}

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", bm.cfg.Headless),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("exclude-switches", "enable-automation"),
		chromedp.WindowSize(1920, 1080),
	)

	if bm.cfg.ChromePath != "" {
		opts = append(opts, chromedp.ExecPath(bm.cfg.ChromePath))
	}
	if bm.cfg.UserDataDir != "" {
		opts = append(opts, chromedp.UserDataDir(bm.cfg.UserDataDir))
	}

	if bm.cfg.RemoteDebugURL != "" {
		allocCtx, cancel := chromedp.NewRemoteAllocator(ctx, bm.cfg.RemoteDebugURL)
		bm.allocCtx = allocCtx
		bm.allocCancel = cancel
	} else {
		bm.allocCtx, bm.allocCancel = chromedp.NewExecAllocator(ctx, opts...)
	}

	bm.browserCtx, bm.browserCancel = chromedp.NewContext(bm.allocCtx,
		chromedp.WithLogf(func(s string, i ...interface{}) {
			if bm.Logf != nil {
				bm.Logf("[chromium] "+s, i...)
			}
		}),
	)

	//  Use bm.browserCtx directly — a derived timeout context would be cancelled
	//  when this function returns (defer timeoutCancel), which chromedp interprets
	//  as a request to tear down the browser session. Timeouts for individual
	//  operations are handled by the caller-supplied ctx via the allocCtx.
	if err := chromedp.Run(bm.browserCtx, chromedp.Navigate("about:blank")); err != nil {
		bm.browserCancel()
		bm.allocCancel()
		return fmt.Errorf("browser launch failed: %w", err)
	}

	bm.Logf("[browser] Chromium launched successfully (headless=%v)", bm.cfg.Headless)
	return nil
}

func (bm *BrowserManager) Authenticate(ctx context.Context) (*BrowserSession, error) {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	if bm.browserCtx == nil {
		return nil, fmt.Errorf("browser not launched, call Launch() first")
	}

	//  Use bm.browserCtx directly to avoid the timeout context being cancelled
	//  when this function returns, which would close the browser session.
	//  Individual operation timeouts are bounded by the polling loop below.

	if bm.cfg.CookieString != "" || bm.cfg.SessionToken != "" {
		if err := bm.injectCookies(bm.browserCtx); err != nil {
			bm.Logf("[browser] cookie injection warning: %v", err)
		}
	}

	bm.Logf("[browser] navigating to chatgpt.com...")
	if err := chromedp.Run(bm.browserCtx,
		chromedp.Navigate("https://chatgpt.com"),
		chromedp.WaitReady("body", chromedp.ByQuery),
	); err != nil {
		return nil, fmt.Errorf("navigate to chatgpt.com: %w", err)
	}

	//  Poll for login: check localStorage/sessionStorage for access token every
	//  3 seconds.  This window lets the user manually log in to chatgpt.com
	//  in the visible Chrome window before we capture the session.
	bm.Logf("[browser] waiting for user login (timeout=%s)...", bm.cfg.Timeout)
	deadline, _ := ctx.Deadline()
	if deadline.IsZero() {
		deadline = time.Now().Add(bm.cfg.Timeout)
	}
	var session *BrowserSession
	var err error
	pollStart := time.Now()
	for time.Until(deadline) > 3*time.Second {
		var s *BrowserSession
		s, err = bm.extractSession(bm.browserCtx)
		if err == nil && s != nil && s.AccessToken != "" {
			session = s
			break
		}
		elapsed := int(time.Since(pollStart).Seconds())
		bm.Logf("[browser] not logged in yet (elapsed=%ds), waiting for login...", elapsed)
		time.Sleep(3 * time.Second)
	}

	if session == nil {
		//  Last attempt even if polling timed out — partial session is still useful
		//  (device ID, UA, build hash, cookies).
		session, err = bm.extractSession(bm.browserCtx)
		if err != nil {
			return nil, fmt.Errorf("extract session: %w", err)
		}
		bm.Logf("[browser] login timed out, continuing with partial session (access_token may be empty)")
	} else {
		bm.Logf("[browser] login detected after %s", time.Since(pollStart).Truncate(time.Second))
	}

	bm.session = session
	bm.ready = true
	bm.Logf("[browser] authentication complete, device_id=%s build_hash=%s", session.DeviceID, session.BuildHash)
	return session, nil
}

func (bm *BrowserManager) injectCookies(ctx context.Context) error {
	cookies := make([]*network.CookieParam, 0)

	if bm.cfg.SessionToken != "" {
		cookies = append(cookies, &network.CookieParam{
			Name:     "__Secure-next-auth.session-token",
			Value:    bm.cfg.SessionToken,
			URL:      "https://chatgpt.com",
			Secure:   true,
			HTTPOnly: true,
			SameSite: network.CookieSameSiteLax,
		})
	}

	if bm.cfg.CookieString != "" {
		for _, part := range strings.Split(bm.cfg.CookieString, ";") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			kv := strings.SplitN(part, "=", 2)
			if len(kv) != 2 {
				continue
			}
			cookies = append(cookies, &network.CookieParam{
				Name:  strings.TrimSpace(kv[0]),
				Value: strings.TrimSpace(kv[1]),
				URL:   "https://chatgpt.com",
			})
		}
	}

	if len(cookies) > 0 {
		return chromedp.Run(ctx, storage.SetCookies(cookies))
	}
	return nil
}

func (bm *BrowserManager) extractSession(ctx context.Context) (*BrowserSession, error) {
	session := &BrowserSession{}

	extractJS := `
	(() => {
		const result = {};
		try {
			for (let i = 0; i < localStorage.length; i++) {
				const key = localStorage.key(i);
				const val = localStorage.getItem(key);
				if (val && val.startsWith('eyJ')) {
					result.accessToken = val;
					break;
				}
			}
		} catch(e) {}
		if (!result.accessToken) {
			try {
				for (let i = 0; i < sessionStorage.length; i++) {
					const key = sessionStorage.key(i);
					const val = sessionStorage.getItem(key);
					if (val && val.startsWith('eyJ')) {
						result.accessToken = val;
						break;
					}
				}
			} catch(e) {}
		}
		try {
			const did = localStorage.getItem('oai-did');
			if (did) result.deviceId = did;
		} catch(e) {}
		try {
			const scripts = document.querySelectorAll('script[src*="_next/static"]');
			for (const s of scripts) {
				const src = s.getAttribute('src');
				const match = src.match(/_next\/static\/([^/]+)/);
				if (match) { result.buildHash = match[1]; break; }
			}
		} catch(e) {}
		try {
			const metas = document.querySelectorAll('meta[name="next-deployment-id"]');
			if (metas.length > 0) result.dpl = metas[0].getAttribute('content');
		} catch(e) {}
		result.userAgent = navigator.userAgent;
		result.screenWidth = screen.width;
		result.screenHeight = screen.height;
		result.pixelRatio = window.devicePixelRatio;
		return JSON.stringify(result);
	})()
	`

	var resultStr string
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(extractJS, &resultStr),
	); err != nil {
		return nil, fmt.Errorf("extract session data: %w", err)
	}

	var data map[string]interface{}
	if err := json.Unmarshal([]byte(resultStr), &data); err != nil {
		return nil, fmt.Errorf("parse session data: %w", err)
	}

	if at, ok := data["accessToken"].(string); ok && at != "" {
		session.AccessToken = at
	}
	if did, ok := data["deviceId"].(string); ok && did != "" {
		session.DeviceID = did
	}
	if bh, ok := data["buildHash"].(string); ok && bh != "" {
		session.BuildHash = bh
	}
	if ua, ok := data["userAgent"].(string); ok && ua != "" {
		session.UserAgent = ua
	}
	if dpl, ok := data["dpl"].(string); ok && dpl != "" {
		session.DPL = dpl
	}
	if sw, ok := data["screenWidth"].(float64); ok {
		session.ScreenWidth = int(sw)
	}
	if sh, ok := data["screenHeight"].(float64); ok {
		session.ScreenHeight = int(sh)
	}
	if pr, ok := data["pixelRatio"].(float64); ok {
		session.PixelRatio = pr
	}

	var cookies []*network.Cookie
	if err := chromedp.Run(ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			var err error
			cookies, err = network.GetCookies().Do(ctx)
			return err
		}),
	); err == nil {
		var cookieParts []string
		for _, c := range cookies {
			if strings.HasSuffix(c.Domain, "chatgpt.com") || strings.HasSuffix(c.Domain, ".openai.com") {
				cookieParts = append(cookieParts, c.Name+"="+c.Value)
			}
		}
		session.CookieString = strings.Join(cookieParts, "; ")
	}

	return session, nil
}

func (bm *BrowserManager) SolveTurnstile(ctx context.Context) (string, error) {
	bm.mu.Lock()
	bCtx := bm.browserCtx
	bm.mu.Unlock()

	if bCtx == nil {
		return "", fmt.Errorf("browser not launched")
	}

	timeoutCtx, cancel := context.WithTimeout(bCtx, 30*time.Second)
	defer cancel()

	solveJS := `
	(() => {
		return new Promise((resolve, reject) => {
			const timeout = setTimeout(() => reject(new Error('turnstile solve timeout')), 25000);
			try {
				const check = () => {
					if (typeof turnstile !== 'undefined' && turnstile.getResponse) {
						const resp = turnstile.getResponse();
						if (resp) { clearTimeout(timeout); resolve(resp); return true; }
					}
					return false;
				};
				if (check()) return;
				const interval = setInterval(() => {
					if (check()) clearInterval(interval);
				}, 500);
			} catch(e) {
				clearTimeout(timeout);
				reject(e);
			}
		});
	})()
	`

	var result string
	if err := chromedp.Run(timeoutCtx,
		chromedp.Evaluate(solveJS, &result, chromedp.EvalAsValue),
	); err != nil {
		return "", fmt.Errorf("turnstile solve: %w", err)
	}

	return result, nil
}

func (bm *BrowserManager) FetchInBrowser(ctx context.Context, method, url string, headers map[string]string, body string) (int, string, map[string]string, error) {
	bm.mu.Lock()
	bCtx := bm.browserCtx
	bm.mu.Unlock()

	if bCtx == nil {
		return 0, "", nil, fmt.Errorf("browser not launched")
	}

	timeoutCtx, cancel := context.WithTimeout(bCtx, 120*time.Second)
	defer cancel()

	headersJSON, _ := json.Marshal(headers)

	bodyOpt := ""
	if body != "" {
		escaped, _ := json.Marshal(body)
		bodyOpt = fmt.Sprintf("opts.body = %s;", string(escaped))
	}

	fetchJS := fmt.Sprintf(`
	(() => {
		const headers = %s;
		const opts = { method: '%s', headers: headers, credentials: 'include' };
		%s
		return fetch('%s', opts).then(async resp => {
			const body = await resp.text();
			const respHeaders = {};
			resp.headers.forEach((v, k) => { respHeaders[k] = v; });
			return JSON.stringify({ status: resp.status, body: body, headers: respHeaders });
		});
	})()
	`, string(headersJSON), method, bodyOpt, url)

	var resultStr string
	if err := chromedp.Run(timeoutCtx,
		chromedp.Evaluate(fetchJS, &resultStr, chromedp.EvalAsValue),
	); err != nil {
		return 0, "", nil, fmt.Errorf("browser fetch: %w", err)
	}

	var resp struct {
		Status  int               `json:"status"`
		Body    string            `json:"body"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal([]byte(resultStr), &resp); err != nil {
		return 0, resultStr, nil, fmt.Errorf("parse fetch response: %w", err)
	}

	return resp.Status, resp.Body, resp.Headers, nil
}

func (bm *BrowserManager) RefreshAccessToken(ctx context.Context) (string, time.Time, error) {
	bm.mu.Lock()
	bCtx := bm.browserCtx
	bm.mu.Unlock()

	if bCtx == nil {
		return "", time.Time{}, fmt.Errorf("browser not launched")
	}

	timeoutCtx, cancel := context.WithTimeout(bCtx, 30*time.Second)
	defer cancel()

	refreshJS := `
	(() => {
		return fetch('/api/auth/session', { credentials: 'include' })
			.then(r => r.ok ? r.json() : Promise.reject(new Error('HTTP ' + r.status)))
			.then(data => JSON.stringify(data));
	})()
	`

	var resultStr string
	if err := chromedp.Run(timeoutCtx,
		chromedp.Evaluate(refreshJS, &resultStr, chromedp.EvalAsValue),
	); err != nil {
		return "", time.Time{}, fmt.Errorf("browser session refresh: %w", err)
	}

	var out struct {
		AccessToken string `json:"accessToken"`
		Expires     string `json:"expires"`
	}
	if err := json.Unmarshal([]byte(resultStr), &out); err != nil {
		return "", time.Time{}, fmt.Errorf("parse session response: %w", err)
	}
	if out.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("session response missing accessToken")
	}

	var expiresAt time.Time
	if out.Expires != "" {
		if t, e := time.Parse(time.RFC3339, out.Expires); e == nil {
			expiresAt = t
		}
	}
	if expiresAt.IsZero() {
		expiresAt = jwtExpTime(out.AccessToken)
	}

	bm.mu.Lock()
	if bm.session != nil {
		bm.session.AccessToken = out.AccessToken
	}
	bm.mu.Unlock()

	return out.AccessToken, expiresAt, nil
}

func (bm *BrowserManager) SyncVersionInfo(ctx context.Context) (buildHash, buildNumber, dpl string, err error) {
	bm.mu.Lock()
	bCtx := bm.browserCtx
	bm.mu.Unlock()

	if bCtx == nil {
		return "", "", "", fmt.Errorf("browser not launched")
	}

	timeoutCtx, cancel := context.WithTimeout(bCtx, 30*time.Second)
	defer cancel()

	syncJS := `
	(() => {
		const result = {};
		try {
			const scripts = document.querySelectorAll('script[src*="_next/static"]');
			for (const s of scripts) {
				const src = s.getAttribute('src');
				const match = src.match(/_next\/static\/([^/]+)/);
				if (match) { result.buildHash = match[1]; break; }
			}
		} catch(e) {}
		try {
			const nd = document.getElementById('__NEXT_DATA__');
			if (nd) {
				const data = JSON.parse(nd.textContent);
				if (data.buildId) result.buildHash = data.buildId;
			}
		} catch(e) {}
		try {
			const metas = document.querySelectorAll('meta[name="next-deployment-id"]');
			if (metas.length > 0) result.dpl = metas[0].getAttribute('content');
		} catch(e) {}
		return JSON.stringify(result);
	})()
	`

	var resultStr string
	if err := chromedp.Run(timeoutCtx,
		chromedp.Evaluate(syncJS, &resultStr),
	); err != nil {
		return "", "", "", fmt.Errorf("sync version: %w", err)
	}

	var data map[string]string
	if err := json.Unmarshal([]byte(resultStr), &data); err != nil {
		return "", "", "", fmt.Errorf("parse version data: %w", err)
	}

	bh := data["buildHash"]
	bn := data["buildNumber"]
	d := data["dpl"]

	if bh != "" && !strings.HasPrefix(bh, "prod-") {
		bh = "prod-" + bh
	}

	return bh, bn, d, nil
}

func (bm *BrowserManager) Close() {
	bm.mu.Lock()
	defer bm.mu.Unlock()

	if bm.browserCancel != nil {
		bm.browserCancel()
		bm.browserCancel = nil
	}
	if bm.allocCancel != nil {
		bm.allocCancel()
		bm.allocCancel = nil
	}
	bm.ready = false
	bm.session = nil
	bm.Logf("[browser] closed")
}

func jwtExpTime(token string) time.Time {
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
