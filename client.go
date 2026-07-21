package sentinel

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/imroc/req/v3"
)

const (
	tlsFingerprintChromeVersion = "131"

	defaultUA          = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + tlsFingerprintChromeVersion + ".0.0.0 Safari/537.36"
	defaultBuildHash   = "prod-81e0c5cdf6140e8c5db714d613337f4aeab94029"
	defaultBuildNumber = "6128297"
	defaultLang        = "zh-CN"
	defaultModel       = "gpt-5-5-thinking"
	deviceIDFile       = "device_id.json"
)

type deviceIDEntry struct {
	DeviceID  string    `json:"device_id"`
	CreatedAt time.Time `json:"created_at"`
}

var deviceIDCache struct {
	mu    sync.RWMutex
	cache map[string]string
}

func init() {
	deviceIDCache.cache = make(map[string]string)
}

func loadOrGenerateDeviceID(token, dataDir string) string {
	key := tokenHash(token)

	deviceIDCache.mu.RLock()
	if cached, ok := deviceIDCache.cache[key]; ok && cached != "" {
		deviceIDCache.mu.RUnlock()
		return cached
	}
	deviceIDCache.mu.RUnlock()

	if dataDir != "" {
		filePath := filepath.Join(dataDir, deviceIDFile)
		if data, err := os.ReadFile(filePath); err == nil {
			var entries map[string]deviceIDEntry
			if json.Unmarshal(data, &entries) == nil {
				if entry, ok := entries[key]; ok && entry.DeviceID != "" {
					deviceIDCache.mu.Lock()
					deviceIDCache.cache[key] = entry.DeviceID
					deviceIDCache.mu.Unlock()
					return entry.DeviceID
				}
			}
		}
	}

	newID := GenerateUUID()

	deviceIDCache.mu.Lock()
	deviceIDCache.cache[key] = newID
	deviceIDCache.mu.Unlock()

	if dataDir != "" {
		_ = os.MkdirAll(dataDir, 0755)
		filePath := filepath.Join(dataDir, deviceIDFile)
		entries := make(map[string]deviceIDEntry)
		if data, err := os.ReadFile(filePath); err == nil {
			json.Unmarshal(data, &entries)
		}
		entries[key] = deviceIDEntry{DeviceID: newID, CreatedAt: time.Now()}
		if data, err := json.MarshalIndent(entries, "", "  "); err == nil {
			os.WriteFile(filePath, data, 0644)
		}
	}

	return newID
}

func tokenHash(token string) string {
	if len(token) > 32 {
		return token[:32]
	}
	return token
}

type Client struct {
	httpClient  *req.Client
	bearerToken string
	cookieStr   string
	userAgent   string
	deviceID    string
	buildHash   string
	buildNumber string
	language    string
	csrfToken   string
	sessionID   string
	imageDir    string
	startTime   time.Time

	conversationID  string
	parentMessageID string
	model           string
	tempMode        bool
	turnCount       int

	browserMgr      *BrowserManager
	useBrowserProxy bool
	dataDir         string

	screenWidth  int
	screenHeight int
	pixelRatio   float64
	pageWidth    int
	pageHeight   int

	Logf LogFunc

	DisableAutoImage bool
	StreamRecorder   *StreamRecorder
}

func NewClient(cfg Config) *Client {
	ua := orDefault(cfg.UserAgent, defaultUA)

	deviceID := cfg.DeviceID
	if deviceID == "" {
		deviceID = loadOrGenerateDeviceID(cfg.BearerToken, cfg.DataDir)
	}

	var screenWidth, screenHeight int
	var pixelRatio float64
	var pageWidth, pageHeight int

	if cfg.BrowserMgr != nil && cfg.BrowserMgr.IsReady() {
		session := cfg.BrowserMgr.GetSession()
		if session != nil {
			if session.UserAgent != "" {
				ua = session.UserAgent
			}
			if session.DeviceID != "" {
				deviceID = session.DeviceID
			}
			if session.BuildHash != "" {
				cfg.BuildHash = session.BuildHash
			}
			if session.BuildNumber != "" {
				cfg.BuildNumber = session.BuildNumber
			}
			if session.CookieString != "" {
				cfg.CookieString = session.CookieString
			}
			if session.AccessToken != "" && cfg.BearerToken == "" {
				cfg.BearerToken = session.AccessToken
			}
			screenWidth = session.ScreenWidth
			screenHeight = session.ScreenHeight
			pixelRatio = session.PixelRatio
			if session.DPL != "" {
				SetDPL(session.DPL)
			}
		}
	}

	if screenWidth == 0 {
		screenWidth = pickRandom([]int{1366, 1440, 1536, 1600, 1680, 1920, 2560})
	}
	if screenHeight == 0 {
		screenHeight = pickRandom([]int{768, 900, 864, 900, 1050, 1080, 1440})
	}
	if pixelRatio == 0 {
		pixelRatio = pickRandomFloat([]float64{1.0, 1.25, 1.5, 1.75, 2.0})
	}
	pageWidth = screenWidth - pickRandom([]int{0, 17, 24, 30})
	pageHeight = screenHeight - pickRandom([]int{100, 110, 120, 130})

	c := &Client{
		bearerToken:     cfg.BearerToken,
		cookieStr:       cfg.CookieString,
		userAgent:       ua,
		deviceID:        deviceID,
		buildHash:       orDefault(cfg.BuildHash, defaultBuildHash),
		buildNumber:     orDefault(cfg.BuildNumber, defaultBuildNumber),
		language:        orDefault(cfg.Language, defaultLang),
		csrfToken:       cfg.CSRFToken,
		imageDir:        orDefault(cfg.ImageDir, "images"),
		model:           orDefault(cfg.Model, defaultModel),
		parentMessageID: "client-created-root",
		sessionID:       GenerateUUID(),
		startTime:       time.Now(),
		tempMode:        cfg.TempMode,
		browserMgr:      cfg.BrowserMgr,
		useBrowserProxy: cfg.UseBrowserProxy,
		dataDir:         cfg.DataDir,
		screenWidth:     screenWidth,
		screenHeight:    screenHeight,
		pixelRatio:      pixelRatio,
		pageWidth:       pageWidth,
		pageHeight:      pageHeight,
		Logf:            log.Printf,
	}

	httpC := req.C().
		SetBaseURL("https://chatgpt.com").
		SetCommonHeaders(c.commonHeaders()).
		ImpersonateChrome()

	if cfg.BrowserMgr != nil && cfg.BrowserMgr.IsReady() {
		session := cfg.BrowserMgr.GetSession()
		if session != nil && session.UserAgent != "" {
			if v := extractChromeVersion(session.UserAgent); v != "" {
				if v != tlsFingerprintChromeVersion {
					c.logf("[client] WARNING: browser Chrome/%s vs TLS fingerprint Chrome/%s mismatch; set USE_BROWSER_PROXY=true for full stealth", v, tlsFingerprintChromeVersion)
				} else {
					c.logf("[client] browser Chrome/%s matches TLS fingerprint ✓", v)
				}
			}
		}
	}

	c.httpClient = httpC
	return c
}

func extractChromeVersion(ua string) string {
	parts := strings.Split(ua, "Chrome/")
	if len(parts) < 2 {
		return ""
	}
	sub := strings.Split(parts[1], ".")
	if len(sub) > 0 {
		return sub[0]
	}
	return ""
}

func (c *Client) HTTPClient() *req.Client {
	return c.httpClient
}

func (c *Client) ResetSession() {
	c.conversationID = ""
	c.parentMessageID = "client-created-root"
	c.turnCount = 0
}

func (c *Client) SetModel(model string) { c.model = model }
func (c *Client) GetModel() string      { return c.model }

func (c *Client) SetTempMode(enabled bool) { c.tempMode = enabled }

func (c *Client) SetDisableAutoImage(disabled bool) { c.DisableAutoImage = disabled }

func (c *Client) SetBearerToken(token string) {
	c.bearerToken = token
	c.httpClient.SetCommonHeader("Authorization", "Bearer "+token)
}

func (c *Client) SetCSRFToken(token string) {
	c.csrfToken = token
	if token == "" {
		c.httpClient.SetCommonHeader("X-CSRF-Token", "")
		c.httpClient.SetCommonHeader("X-XSRF-Token", "")
		return
	}
	c.httpClient.SetCommonHeader("X-CSRF-Token", token)
	c.httpClient.SetCommonHeader("X-XSRF-Token", token)
}

func (c *Client) SetConversationID(id string) { c.conversationID = id }

func (c *Client) SetParentMessageID(id string) { c.parentMessageID = id }

func (c *Client) GetSessionInfo() SessionInfo {
	return SessionInfo{
		ConversationID:  c.conversationID,
		ParentMessageID: c.parentMessageID,
		Model:           c.model,
		TempMode:        c.tempMode,
		TurnCount:       c.turnCount,
	}
}

func (c *Client) logf(format string, args ...interface{}) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

func (c *Client) commonHeaders() map[string]string {
	chromeMajor := extractChromeVersion(c.userAgent)
	if chromeMajor == "" {
		chromeMajor = tlsFingerprintChromeVersion
	}

	secCHUA := fmt.Sprintf(`"Chromium";v="%s", "Not-A.Brand";v="24", "Google Chrome";v="%s"`, chromeMajor, chromeMajor)
	secCHUAFull := fmt.Sprintf(`"Chromium";v="%s.0.0.0", "Not-A.Brand";v="24.0.0.0", "Google Chrome";v="%s.0.0.0"`, chromeMajor, chromeMajor)

	h := map[string]string{
		"Authorization":               "Bearer " + c.bearerToken,
		"User-Agent":                  c.userAgent,
		"Accept-Language":             c.language + ",zh;q=0.9,en;q=0.8,en-GB;q=0.7,en-US;q=0.6",
		"Accept-Encoding":             "gzip, deflate, br, zstd",
		"oai-language":                c.language,
		"oai-device-id":               c.deviceID,
		"oai-session-id":              c.sessionID,
		"oai-client-version":          c.buildHash,
		"oai-client-build-number":     c.buildNumber,
		"Origin":                      "https://chatgpt.com",
		"Referer":                     "https://chatgpt.com/",
		"sec-ch-ua":                   secCHUA,
		"sec-ch-ua-mobile":            "?0",
		"sec-ch-ua-platform":          `"Windows"`,
		"sec-ch-ua-platform-version":  `"19.0.0"`,
		"sec-ch-ua-arch":              `"x86"`,
		"sec-ch-ua-bitness":           `"64"`,
		"sec-ch-ua-model":             `""`,
		"sec-ch-ua-full-version":      fmt.Sprintf(`"%s.0.0.0"`, chromeMajor),
		"sec-ch-ua-full-version-list": secCHUAFull,
		"sec-fetch-dest":              "empty",
		"sec-fetch-mode":              "cors",
		"sec-fetch-site":              "same-origin",
	}
	if c.cookieStr != "" {
		h["Cookie"] = c.cookieStr
	}
	if c.csrfToken != "" {
		h["X-CSRF-Token"] = c.csrfToken
		h["X-XSRF-Token"] = c.csrfToken
	}
	return h
}

func (c *Client) jitterSleep() {
	delay := time.Duration(200+rand.Intn(800)) * time.Millisecond
	time.Sleep(delay)
}

func pickRandom(options []int) int {
	return options[rand.Intn(len(options))]
}

func pickRandomFloat(options []float64) float64 {
	return options[rand.Intn(len(options))]
}
