package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Notifier interface {
	NotifyTokenRefreshFailure(tokenID string, err error)
}

type noopNotifier struct{}

func (noopNotifier) NotifyTokenRefreshFailure(string, error) {}

type TelegramNotifier struct {
	botToken string
	chatID   string
	client   *http.Client

	mu        sync.Mutex
	lastSent  map[string]time.Time
	minPeriod time.Duration
}

func NewTelegramNotifier(botToken, chatID string) Notifier {
	botToken = strings.TrimSpace(botToken)
	chatID = strings.TrimSpace(chatID)
	if botToken == "" || chatID == "" {
		return noopNotifier{}
	}
	return &TelegramNotifier{
		botToken:  botToken,
		chatID:    chatID,
		client:    &http.Client{Timeout: 10 * time.Second},
		lastSent:  make(map[string]time.Time),
		minPeriod: time.Hour,
	}
}

func (n *TelegramNotifier) NotifyTokenRefreshFailure(tokenID string, err error) {
	if n == nil {
		return
	}
	if !n.shouldSend(tokenID) {
		return
	}
	message := fmt.Sprintf("Sentinel-Go 提醒：ChatGPT Session Token 刷新 Access Token 失败。\nToken: %s\n错误: %s\n请重新登录 chatgpt.com 并更新 Session Token。", tokenID, err.Error())
	go n.send(message)
}

func (n *TelegramNotifier) shouldSend(tokenID string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := time.Now()
	if last, ok := n.lastSent[tokenID]; ok && now.Sub(last) < n.minPeriod {
		return false
	}
	n.lastSent[tokenID] = now
	return true
}

func (n *TelegramNotifier) send(message string) {
	body, err := json.Marshal(map[string]string{
		"chat_id": n.chatID,
		"text":    message,
	})
	if err != nil {
		log.Printf("[telegram] 构造通知失败: %v", err)
		return
	}
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", n.botToken)
	resp, err := n.client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		log.Printf("[telegram] 发送通知失败: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		log.Printf("[telegram] 发送通知失败 http=%d", resp.StatusCode)
	}
}
