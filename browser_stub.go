//go:build !browser

package sentinel

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
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
	cfg    BrowserConfig
	mu     sync.Mutex
	session *BrowserSession
	ready  bool
	Logf   LogFunc
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
	return fmt.Errorf("browser mode not available: rebuild with -tags=browser to enable Chromium support")
}

func (bm *BrowserManager) Authenticate(ctx context.Context) (*BrowserSession, error) {
	return nil, fmt.Errorf("browser mode not available: rebuild with -tags=browser")
}

func (bm *BrowserManager) SolveTurnstile(ctx context.Context) (string, error) {
	return "", fmt.Errorf("browser mode not available: rebuild with -tags=browser")
}

func (bm *BrowserManager) FetchInBrowser(ctx context.Context, method, url string, headers map[string]string, body string) (int, string, map[string]string, error) {
	return 0, "", nil, fmt.Errorf("browser mode not available: rebuild with -tags=browser")
}

func (bm *BrowserManager) RefreshAccessToken(ctx context.Context) (string, time.Time, error) {
	return "", time.Time{}, fmt.Errorf("browser mode not available: rebuild with -tags=browser")
}

func (bm *BrowserManager) SyncVersionInfo(ctx context.Context) (buildHash, buildNumber, dpl string, err error) {
	return "", "", "", fmt.Errorf("browser mode not available: rebuild with -tags=browser")
}

func (bm *BrowserManager) Close() {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	bm.ready = false
	bm.session = nil
}
