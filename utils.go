package sentinel

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
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

// encodeBase64JSON 将值 JSON 序列化后 Base64 编码（对应 JS 的 O0 函数）
func encodeBase64JSON(v interface{}) string {
	data, _ := json.Marshal(v)
	return base64.StdEncoding.EncodeToString(data)
}

// fnvHash 对应 JS 的 rEe 函数：FNV-1a 变体散列
func fnvHash(s string) string {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	h ^= h >> 16
	h *= 2246822507
	h ^= h >> 13
	h *= 3266489909
	h ^= h >> 16
	return fmt.Sprintf("%08x", h)
}

// buildCfg 构造指纹配置数组（对应 JS 的 buildCfg）
func buildCfg(ua, buildHash, lang, sid string, t0 int64, perfNow float64) []interface{} {
	return []interface{}{
		3000,
		jsDateString(time.Now()),
		int64(4294967296),
		nil,
		ua,
		"",
		buildHash,
		lang,
		"zh-CN,en,en-GB,en-US",
		nil,
		"credentials\u2252[object Navigator]",
		"location",
		"fetch",
		perfNow,
		sid,
		"",
		28,
		t0,
		0, 0, 0, 0, 0, 0, 0,
	}
}

// jsDateString 模拟 JavaScript Date.toString() 的输出格式
func jsDateString(t time.Time) string {
	days := [...]string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
	months := [...]string{"Jan", "Feb", "Mar", "Apr", "May", "Jun",
		"Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}
	name, offset := t.Zone()
	sign := "+"
	if offset < 0 {
		sign = "-"
		offset = -offset
	}
	h := offset / 3600
	m := (offset % 3600) / 60
	return fmt.Sprintf("%s %s %02d %d %02d:%02d:%02d GMT%s%02d%02d (%s)",
		days[t.Weekday()], months[t.Month()-1], t.Day(), t.Year(),
		t.Hour(), t.Minute(), t.Second(), sign, h, m, name)
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

// getFirstStringPart 从 message 的 content.parts[0] 取字符串
func getFirstStringPart(msg map[string]interface{}) string {
	content, ok := msg["content"].(map[string]interface{})
	if !ok {
		return ""
	}
	parts, ok := content["parts"].([]interface{})
	if !ok || len(parts) == 0 {
		return ""
	}
	s, _ := parts[0].(string)
	return s
}

var fileIDRegexp = regexp.MustCompile(`file_[a-f0-9]+`)

func extractFileID(pointer string) string {
	if pointer == "" {
		return ""
	}
	return fileIDRegexp.FindString(pointer)
}
