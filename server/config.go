package server

import (
	"os"
	"strconv"
)

type ServerConfig struct {
	Host string
	Port string

	Authorization string

	DefaultModel string
	TempMode     bool
	ImageDir     string

	TokensFile string

	SessionTTLMinutes int

	BaseURL string

	TokenRefreshAheadSec int

	TelegramBotToken string
	TelegramChatID   string

	BrowserEnabled     bool
	BrowserHeadless    bool
	BrowserChromePath  string
	BrowserUserDataDir string
	BrowserRemoteURL   string
	BrowserTimeoutSec  int
	UseBrowserProxy    bool
	DataDir            string
}

func LoadConfig() ServerConfig {
	return ServerConfig{
		Host:                 getEnv("HOST", "0.0.0.0"),
		Port:                 getEnv("PORT", "5005"),
		Authorization:        getEnv("AUTHORIZATION", ""),
		DefaultModel:         getEnv("DEFAULT_MODEL", "gpt-5-5-thinking"),
		TempMode:             getEnvBool("TEMP_MODE", false),
		ImageDir:             getEnv("IMAGE_DIR", "images"),
		TokensFile:           getEnv("TOKENS_FILE", "tokens.json"),
		SessionTTLMinutes:    getEnvInt("SESSION_TTL_MINUTES", 120),
		BaseURL:              getEnv("BASE_URL", ""),
		TokenRefreshAheadSec: getEnvInt("TOKEN_REFRESH_AHEAD_SEC", 300),
		TelegramBotToken:     getEnv("TELEGRAM_BOT_TOKEN", ""),
		TelegramChatID:       getEnv("TELEGRAM_CHAT_ID", ""),
		BrowserEnabled:       getEnvBool("BROWSER_ENABLED", false),
		BrowserHeadless:      getEnvBool("BROWSER_HEADLESS", true),
		BrowserChromePath:    getEnv("BROWSER_CHROME_PATH", ""),
		BrowserUserDataDir:   getEnv("BROWSER_USER_DATA_DIR", ""),
		BrowserRemoteURL:     getEnv("BROWSER_REMOTE_URL", ""),
		BrowserTimeoutSec:    getEnvInt("BROWSER_TIMEOUT_SEC", 60),
		UseBrowserProxy:      getEnvBool("USE_BROWSER_PROXY", false),
		DataDir:              getEnv("DATA_DIR", ""),
	}
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvBool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func getEnvInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}
