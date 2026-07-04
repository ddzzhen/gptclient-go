package sentinel

import (
	"crypto/rand"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// GenerateUUID 生成 v4 UUID
func GenerateUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func perfNowMs(start time.Time) float64 {
	return float64(time.Since(start).Microseconds()) / 1000.0
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}

func runeSlice(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) > maxRunes {
		r = r[:maxRunes]
	}
	return string(r)
}

func orDefault(val, def string) string {
	if val != "" {
		return val
	}
	return def
}

// getNestedString 从嵌套 map 中按路径取 string 值
func getNestedString(m map[string]interface{}, keys ...string) string {
	current := m
	for i, key := range keys {
		if i == len(keys)-1 {
			s, _ := current[key].(string)
			return s
		}
		next, ok := current[key].(map[string]interface{})
		if !ok {
			return ""
		}
		current = next
	}
	return ""
}

// getMessageText 从 message.content 中提取完整文本。
// 新版 ChatGPT / thinking 模型可能把 final 正文拆到多个 parts，不能只取 parts[0]。
func getMessageText(msg map[string]interface{}) string {
	content, ok := msg["content"].(map[string]interface{})
	if !ok {
		return ""
	}
	return extractContentText(content)
}

// getFirstStringPart 保留旧函数名兼容调用点，实际返回完整文本。
func getFirstStringPart(msg map[string]interface{}) string {
	return getMessageText(msg)
}

func extractContentText(value interface{}) string {
	switch v := value.(type) {
	case string:
		return v
	case []interface{}:
		var b strings.Builder
		for _, item := range v {
			b.WriteString(extractContentText(item))
		}
		return b.String()
	case map[string]interface{}:
		if text, ok := v["text"].(string); ok {
			return text
		}
		if content, ok := v["content"].(string); ok {
			return content
		}
		if parts, ok := v["parts"].([]interface{}); ok {
			return extractContentText(parts)
		}
		if content, ok := v["content"].(map[string]interface{}); ok {
			return extractContentText(content)
		}
	}
	return ""
}

var fileIDRegexp = regexp.MustCompile(`file_[a-f0-9]+`)

// LogContentPreview 打印文本长度与首尾预览（UTF-8 安全截断）。
func LogContentPreview(logf func(string, ...interface{}), tag string, s string) {
	if logf == nil {
		return
	}
	runes := []rune(s)
	n := len(runes)
	if n == 0 {
		logf("[%s] len=0 (empty)", tag)
		return
	}
	const edge = 500
	preview := s
	if n > edge*2 {
		preview = string(runes[:edge]) + "\n...(truncated " + fmt.Sprintf("%d", n-edge*2) + " runes)...\n" + string(runes[n-edge:])
	}
	logf("[%s] len=%d preview:\n%s", tag, n, preview)
}

func extractFileID(pointer string) string {
	if pointer == "" {
		return ""
	}
	return fileIDRegexp.FindString(pointer)
}
