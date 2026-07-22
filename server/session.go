package server

import (
	"log"
	"sync"
	"time"

	sentinel "sentinel-go"
)

type sessionEntry struct {
	client   *sentinel.Client
	lastUsed time.Time
	token    string
}

type SessionManager struct {
	mu         sync.RWMutex
	sessions   map[string]*sessionEntry
	ttl        time.Duration
	cfg        *ServerConfig
	browserMgr *sentinel.BrowserManager
}

func NewSessionManager(cfg *ServerConfig, browserMgr *sentinel.BrowserManager) *SessionManager {
	sm := &SessionManager{
		sessions:   make(map[string]*sessionEntry),
		ttl:        time.Duration(cfg.SessionTTLMinutes) * time.Minute,
		cfg:        cfg,
		browserMgr: browserMgr,
	}
	go sm.cleanupLoop()
	return sm
}

func (sm *SessionManager) GetSession(convID string) (*sessionEntry, bool) {
	if convID == "" {
		return nil, false
	}
	sm.mu.RLock()
	entry, ok := sm.sessions[convID]
	sm.mu.RUnlock()
	return entry, ok
}

func (sm *SessionManager) GetOrCreate(convID, token string) *sessionEntry {
	if convID != "" {
		sm.mu.RLock()
		entry, ok := sm.sessions[convID]
		sm.mu.RUnlock()
		if ok {
			sm.mu.Lock()
			entry.lastUsed = time.Now()
			sm.mu.Unlock()
			return entry
		}
	}

	clientCfg := sentinel.Config{
		BearerToken:     token,
		Model:           sm.cfg.DefaultModel,
		TempMode:        sm.cfg.TempMode,
		ImageDir:        sm.cfg.ImageDir,
		BrowserMgr:      sm.browserMgr,
		UseBrowserProxy: sm.cfg.UseBrowserProxy,
		DataDir:         sm.cfg.DataDir,
		CookieString:    sm.cfg.CookieString,
		DeviceID:        sm.cfg.DeviceID,
		UserAgent:       sm.cfg.UserAgentOverride,
	}

	client := sentinel.NewClient(clientCfg)
	client.SetDisableAutoImage(false)

	entry := &sessionEntry{
		client:   client,
		lastUsed: time.Now(),
		token:    token,
	}
	return entry
}

func (sm *SessionManager) Register(convID string, entry *sessionEntry) {
	if convID == "" {
		return
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	entry.lastUsed = time.Now()
	sm.sessions[convID] = entry
}

func (sm *SessionManager) Delete(convID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.sessions, convID)
}

func (sm *SessionManager) Count() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.sessions)
}

func (sm *SessionManager) cleanupLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		sm.cleanup()
	}
}

func (sm *SessionManager) cleanup() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	now := time.Now()
	removed := 0
	for convID, entry := range sm.sessions {
		if now.Sub(entry.lastUsed) > sm.ttl {
			delete(sm.sessions, convID)
			removed++
		}
	}
	if removed > 0 {
		log.Printf("[session] 清理过期 session %d 个，当前活跃 %d 个", removed, len(sm.sessions))
	}
}
