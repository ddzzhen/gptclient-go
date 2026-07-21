package main

import (
	"context"
	"fmt"
	"log"
	"time"

	sentinel "sentinel-go"
	"sentinel-go/server"
)

func main() {
	cfg := server.LoadConfig()

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
