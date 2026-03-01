package cloudrun

import (
	"fmt"
	"math/rand"
	"sync"
	"time"
)

type Session struct {
	SessionID  string
	URL        string
	Flag       string
	CreatedAt  time.Time
	LastActive time.Time
}

type SessionManager struct {
	mu          sync.RWMutex
	sessions    map[string]*Session
	cloudrun    *CloudRunClient
	stopCh      chan struct{}
	maxSessions int
}

func NewSessionManager(cloudrun *CloudRunClient, maxSessions int) *SessionManager {
	sm := &SessionManager{
		sessions:    make(map[string]*Session),
		cloudrun:    cloudrun,
		stopCh:      make(chan struct{}),
		maxSessions: maxSessions,
	}
	go sm.cleanupLoop()
	return sm
}

// GenerateSessionID generates a short 8 character ID that fits nicely into Cloud Run service naming specs (lowercase letters/numbers only)
func GenerateSessionID() string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

func (sm *SessionManager) CreateSession(url string, sessionID string, flag string) (*Session, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if len(sm.sessions) >= sm.maxSessions {
		return nil, fmt.Errorf("maximum active sessions reached")
	}

	now := time.Now()
	s := &Session{
		SessionID:  sessionID,
		URL:        url,
		Flag:       flag,
		CreatedAt:  now,
		LastActive: now,
	}
	sm.sessions[sessionID] = s
	return s, nil
}

func (sm *SessionManager) GetSession(sessionID string) (*Session, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	s, ok := sm.sessions[sessionID]
	if ok {
		s.LastActive = time.Now()
	}
	return s, ok
}

func (sm *SessionManager) DeleteSession(sessionID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if _, ok := sm.sessions[sessionID]; ok {
		// Asynchronously delete Cloud Run Service
		go sm.cloudrun.DeleteLabContainer(sessionID)
	}
	delete(sm.sessions, sessionID)
}

func (sm *SessionManager) ListActiveSessions() []*Session {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	result := make([]*Session, 0, len(sm.sessions))
	for _, s := range sm.sessions {
		result = append(result, s)
	}
	return result
}

func (sm *SessionManager) cleanupLoop() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-sm.stopCh:
			return
		case <-ticker.C:
			sm.cleanupSessions()
		}
	}
}

func (sm *SessionManager) cleanupSessions() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	now := time.Now()
	for id, s := range sm.sessions {
		totalAge := now.Sub(s.CreatedAt)
		idleAge := now.Sub(s.LastActive)
		if totalAge > 5*time.Minute || idleAge > 5*time.Minute {
			// Auto delete Cloud Run service over budget
			go sm.cloudrun.DeleteLabContainer(id)
			delete(sm.sessions, id)
			fmt.Printf("[CLEANUP] Deleted Cloud Run session %s (total: %v, idle: %v)\n", id, totalAge, idleAge)
		}
	}
}
