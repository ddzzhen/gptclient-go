package server

import (
	"os"
	"strconv"
)

// ServerConfig 服务器配置，全部从环境变量读取
type ServerConfig struct {
	// HTTP 服务
	Host string // 监听地址，默认 0.0.0.0
	Port string // 监听端口，默认 5005

	// 鉴权：调用本服务的 API Key（区别于 ChatGPT Bearer Token）
	// 若为空，则不校验 Authorization 头（直接将传入的 token 当作 ChatGPT token 使用）
	Authorization string

	// ChatGPT 客户端默认参数
	DefaultModel string // 默认模型，默认 gpt-5-5-thinking
	TempMode     bool   // 临时模式（不保存对话历史），默认 false
	ImageDir     string // 图片保存目录，默认 images

	// Token 池
	TokensFile string // Token 持久化文件路径（JSON），默认 tokens.json

	// Session 管理
	SessionTTLMinutes int // Session 不活跃超时（分钟），默认 120

	// 对外地址（可选），用于生成绝对资源链接（图片/PDF 代理 URL）
	// 例如：http://192.168.1.10:5005 或 https://your.domain
	// 若为空，则从请求的 Host / X-Forwarded-Proto 头自动推断
	BaseURL string

	// Session Token 自动刷新：在 AT 过期前多少秒提前用 ST 换 AT，默认 300
	TokenRefreshAheadSec int

	// Telegram 通知：Session Token 刷新失败时提醒人工更新
	TelegramBotToken string
	TelegramChatID   string
}

// LoadConfig 从环境变量加载配置
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
