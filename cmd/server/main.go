package main

import (
	"encoding/json"
	"context"
	"os"
	"fmt"
	"strings"
	"log"
	"time"

	sentinel "sentinel-go"
	"sentinel-go/server"
)

func main() {
	cfg := server.LoadConfig()

	// Load browser session data from JSON file if specified
	if cfg.BrowserSessionFile != "" {
		bsData, err := os.ReadFile(cfg.BrowserSessionFile)
		if err != nil {
			log.Printf("[startup] WARNING: failed to read browser session file %s: %v", cfg.BrowserSessionFile, err)
		} else {
			var bs struct {
				CookieString string `json:"cookie_string"`
				DeviceID     string `json:"device_id"`
				UserAgent    string `json:"user_agent"`
			}
			if err := json.Unmarshal(bsData, &bs); err != nil {
				log.Printf("[startup] WARNING: failed to parse browser session: %v", err)
			} else {
				if cfg.CookieString == "" && bs.CookieString != "" {
					cfg.CookieString = bs.CookieString
				}
				if cfg.DeviceID == "" && bs.DeviceID != "" {
					cfg.DeviceID = bs.DeviceID
				}
				if cfg.UserAgentOverride == "" && bs.UserAgent != "" {
					cfg.UserAgentOverride = bs.UserAgent
				}
				log.Printf("[startup] Browser session loaded: device_id=%s ua=%s", cfg.DeviceID, cfg.UserAgentOverride)
			}
		}
	}

	log.Printf("============================================")
	log.Printf("  sentinel-go API Server")
	log.Printf("  Listen Address : %s:%s", cfg.Host, cfg.Port)
	log.Printf("  Default Model  : %s", cfg.DefaultModel)
	log.Printf("  Temp Mode      : %v", cfg.TempMode)
	log.Printf("  Tokens File    : %s", cfg.TokensFile)
	log.Printf("  Session TTL    : %d min", cfg.SessionTTLMinutes)
	log.Printf("  Browser Mode   : %v (headless=%v, proxy=%v)", cfg.BrowserEnabled, cfg.BrowserHeadless, cfg.UseBrowserProxy)
	if cfg.Authorization != "" {
		log.Printf("  Authorization  : configured (pool mode)")
	} else {
		log.Printf("  Authorization  : not set (direct token mode)")
	}
	if cfg.TelegramBotToken != "" && cfg.TelegramChatID != "" {
		log.Printf("  Telegram Alert : configured")
	} else {
		log.Printf("  Telegram Alert : disabled")
	}
	log.Printf("============================================")

	var browserMgr *sentinel.BrowserManager

	if cfg.BrowserEnabled {
		log.Printf("[startup] Initializing browser manager...")
		browserCfg := sentinel.BrowserConfig{
			Headless:       cfg.BrowserHeadless,
			ChromePath:     cfg.BrowserChromePath,
			UserDataDir:    cfg.BrowserUserDataDir,
			RemoteDebugURL: cfg.BrowserRemoteURL,
			Timeout:        time.Duration(cfg.BrowserTimeoutSec) * time.Second,
		}

		browserMgr = sentinel.NewBrowserManager(browserCfg)

		ctx := context.Background()
		if err := browserMgr.Launch(ctx); err != nil {
			log.Printf("[startup] WARNING: Browser launch failed: %v", err)
			log.Printf("[startup] Falling back to non-browser mode")
			browserMgr = nil
		} else {
			log.Printf("[startup] Browser launched successfully")

			session, err := browserMgr.Authenticate(ctx)
			if err != nil {
				log.Printf("[startup] WARNING: Browser authentication failed: %v", err)
				log.Printf("[startup] Will attempt authentication on first request")
			} else if session != nil {
				log.Printf("[startup] Browser authenticated: device_id=%s ua=%s", session.DeviceID, session.UserAgent)
			}
		}
	}

	notifier := server.NewTelegramNotifier(cfg.TelegramBotToken, cfg.TelegramChatID)
	pool := server.NewTokenPool(cfg.TokensFile, time.Duration(cfg.TokenRefreshAheadSec)*time.Second, notifier)

	// Inject browser-obtained access token into the pool so pool-mode middleware
	// and auth-retry logic can use it.  Also extract the session token from the
	// cookie string for automatic refresh.
	if browserMgr != nil && browserMgr.IsReady() {
		if bs := browserMgr.GetSession(); bs != nil && bs.AccessToken != "" {
			chunk := bs.AccessToken
			// Try to extract the session token from the cookie string for refresh support.
			for _, part := range strings.Split(bs.CookieString, ";") {
				part = strings.TrimSpace(part)
				if strings.HasPrefix(part, "__Secure-next-auth.session-token=") {
					st := strings.TrimPrefix(part, "__Secure-next-auth.session-token=")
					if st != "" {
						chunk = bs.AccessToken + "----" + st
						break
					}
				}
			}
			added := pool.Add(chunk)
			log.Printf("[startup] Browser token injected into pool (added=%d)", added)
		} else {
			log.Printf("[startup] Browser session has no access token; pool will use existing tokens")
		}
	}

	total, valid, _ := pool.Stats()
	log.Printf("[startup] Token pool: total=%d, valid=%d", total, valid)

	session := server.NewSessionManager(&cfg, browserMgr)
	log.Printf("[startup] Session manager initialized (TTL=%d min)", cfg.SessionTTLMinutes)

	r := server.NewRouter(&cfg, pool, session)

	addr := fmt.Sprintf("%s:%s", cfg.Host, cfg.Port)
	log.Printf("[startup] Listening on http://%s", addr)
	log.Printf("[startup] API endpoint: http://%s/v1/chat/completions", addr)

	if err := r.Run(addr); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
