package server

import (
	"errors"
	"log"
	"strings"
	"sync"
	"time"
)

// TokenPool Token 池：持久化为 JSON；支持 AT、ST 或二者并存，ST 可自动续期 AT。
type TokenPool struct {
	mu           sync.Mutex
	entries      []storedToken
	errorKeys    map[string]bool
	roundIdx     int
	tokensFile   string
	refreshAhead time.Duration
}

// NewTokenPool 创建并从 JSON 文件加载 Token 池（兼容旧版行格式）。
func NewTokenPool(tokensFile string, refreshAhead time.Duration) *TokenPool {
	if refreshAhead <= 0 {
		refreshAhead = 5 * time.Minute
	}
	tp := &TokenPool{
		errorKeys:    make(map[string]bool),
		tokensFile:   tokensFile,
		refreshAhead: refreshAhead,
	}
	tp.loadFromFile()
	return tp
}

func (tp *TokenPool) loadFromFile() {
	tokens := loadTokensFromFile(tp.tokensFile)
	migrated := false
	if len(tokens) == 0 && strings.HasSuffix(tp.tokensFile, ".json") {
		legacy := strings.TrimSuffix(tp.tokensFile, ".json") + ".txt"
		if legacy != tp.tokensFile {
			if legacyTokens := loadTokensFromFile(legacy); len(legacyTokens) > 0 {
				tokens = legacyTokens
				migrated = true
				log.Printf("[token-pool] 从旧文件 %s 迁移 %d 条凭证", legacy, len(tokens))
			}
		}
	}
	var atN, stN int
	for _, t := range tokens {
		if t.AccessToken != "" {
			atN++
		}
		if t.SessionToken != "" {
			stN++
		}
		tp.entries = append(tp.entries, t)
	}
	if len(tp.entries) > 0 {
		log.Printf("[token-pool] 已加载 %d 条凭证 (AT=%d, ST=%d) ← %s", len(tp.entries), atN, stN, tp.tokensFile)
		if migrated {
			_ = saveTokensToFile(tp.tokensFile, tp.entries)
		}
	}
}

func (tp *TokenPool) persistLocked() {
	if err := saveTokensToFile(tp.tokensFile, tp.entries); err != nil {
		log.Printf("[token-pool] 保存失败: %v", err)
	}
}

func (tp *TokenPool) refreshEntry(e *storedToken) (string, error) {
	if e.SessionToken == "" {
		if e.AccessToken == "" {
			return "", errors.New("no token")
		}
		return e.AccessToken, nil
	}
	at, exp, err := RefreshATFromSession(e.SessionToken)
	if err != nil {
		return "", err
	}
	e.AccessToken = at
	e.ExpiresAt = exp
	e.UpdatedAt = time.Now()
	log.Printf("[token-pool] ST→AT 刷新成功, 过期时间 %s", exp.Format(time.RFC3339))
	tp.persistLocked()
	return at, nil
}

func (tp *TokenPool) ensureFresh(e *storedToken) (string, error) {
	if e.SessionToken != "" {
		need := e.AccessToken == "" || e.ExpiresAt.IsZero() || time.Now().Add(tp.refreshAhead).After(e.ExpiresAt)
		if need {
			return tp.refreshEntry(e)
		}
		return e.AccessToken, nil
	}
	if e.AccessToken == "" {
		return "", errors.New("empty access token")
	}
	if e.ExpiresAt.IsZero() {
		e.ExpiresAt = parseJWTExp(e.AccessToken)
	}
	return e.AccessToken, nil
}

// Pick 轮询选取可用 AT；含 ST 的条目会在过期前自动刷新。
func (tp *TokenPool) Pick() (string, bool) {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	n := len(tp.entries)
	if n == 0 {
		return "", false
	}

	for i := 0; i < n; i++ {
		idx := (tp.roundIdx + i) % n
		e := &tp.entries[idx]
		if tp.errorKeys[e.dedupKey()] {
			continue
		}
		at, err := tp.ensureFresh(e)
		if err != nil {
			log.Printf("[token-pool] 刷新失败 key=%s: %v", e.dedupKey(), err)
			tp.errorKeys[e.dedupKey()] = true
			continue
		}
		tp.roundIdx = (idx + 1) % n
		return at, true
	}
	return "", false
}

// TryRefreshAT 强制用 ST 刷新与 currentAT 对应的条目（401 重试用）。
func (tp *TokenPool) TryRefreshAT(currentAT string) (string, bool) {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	for i := range tp.entries {
		e := &tp.entries[i]
		if e.SessionToken == "" {
			continue
		}
		if e.AccessToken != "" && e.AccessToken != currentAT {
			continue
		}
		at, err := tp.refreshEntry(e)
		if err != nil {
			log.Printf("[token-pool] 强制刷新失败: %v", err)
			return "", false
		}
		delete(tp.errorKeys, e.dedupKey())
		return at, true
	}
	return "", false
}

// mergeToken 将新凭证合并进已有条目（同 ST 或同 AT 则更新）。
func mergeToken(existing *storedToken, incoming storedToken) {
	if incoming.AccessToken != "" {
		existing.AccessToken = incoming.AccessToken
		existing.ExpiresAt = incoming.ExpiresAt
		if existing.ExpiresAt.IsZero() {
			existing.ExpiresAt = parseJWTExp(incoming.AccessToken)
		}
	}
	if incoming.SessionToken != "" {
		existing.SessionToken = incoming.SessionToken
	}
	existing.UpdatedAt = time.Now()
	existing.assignID()
}

// Add 解析并添加凭证；整文件 JSON 重写保存（同时保留 access + session）。
func (tp *TokenPool) Add(chunks ...string) int {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	byKey := make(map[string]int, len(tp.entries))
	for i, e := range tp.entries {
		byKey[e.dedupKey()] = i
	}

	added := 0
	for _, raw := range chunks {
		incoming, ok := parseCredentialInput(raw)
		if !ok {
			continue
		}

		key := incoming.dedupKey()
		if key == "" {
			continue
		}

		// 同 session 或同 access 则更新，避免重复条目
		merged := false
		for i := range tp.entries {
			e := &tp.entries[i]
			if incoming.SessionToken != "" && e.SessionToken == incoming.SessionToken {
				mergeToken(e, incoming)
				byKey[e.dedupKey()] = i
				merged = true
				break
			}
			if incoming.AccessToken != "" && e.AccessToken == incoming.AccessToken {
				mergeToken(e, incoming)
				byKey[e.dedupKey()] = i
				merged = true
				break
			}
		}
		if merged {
			added++
			delete(tp.errorKeys, key)
			continue
		}

		if _, dup := byKey[key]; dup {
			continue
		}

		tp.entries = append(tp.entries, incoming)
		byKey[key] = len(tp.entries) - 1
		added++
		delete(tp.errorKeys, key)
	}

	if added > 0 || len(tp.entries) > 0 {
		tp.persistLocked()
	}
	return added
}

// Clear 清空池与文件。
func (tp *TokenPool) Clear() {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	tp.entries = nil
	tp.errorKeys = make(map[string]bool)
	tp.roundIdx = 0
	_ = saveTokensToFile(tp.tokensFile, nil)
}

// MarkError 标记失效（按当前 AT 匹配条目）。
func (tp *TokenPool) MarkError(at string) {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	for i := range tp.entries {
		if tp.entries[i].AccessToken == at {
			tp.errorKeys[tp.entries[i].dedupKey()] = true
			return
		}
	}
	tp.errorKeys["at:"+at] = true
}

// Stats 返回 total / valid / errored。
func (tp *TokenPool) Stats() (total, valid, errored int) {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	total = len(tp.entries)
	errored = len(tp.errorKeys)
	valid = total - errored
	if valid < 0 {
		valid = 0
	}
	return
}

// ErrorTokens 返回失效条目的 dedupKey 列表。
func (tp *TokenPool) ErrorTokens() []string {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	result := make([]string, 0, len(tp.errorKeys))
	for k := range tp.errorKeys {
		result = append(result, k)
	}
	return result
}
