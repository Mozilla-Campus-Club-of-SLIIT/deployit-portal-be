package docker

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"
	"github.com/docker/docker/api/types/container"
	"github.com/google/uuid"
)

type Session struct {
SessionID     string
ContainerID   string
HostPort      int
CreatedAt     time.Time
LastActive    time.Time
}

type SessionManager struct {
	mu         sync.RWMutex
	sessions   map[string]*Session
	docker     *DockerClient
	stopCh     chan struct{}
	maxSessions int
}

func NewSessionManager(docker *DockerClient, maxSessions int) *SessionManager {
	sm := &SessionManager{
		sessions: make(map[string]*Session),
		docker: docker,
		stopCh: make(chan struct{}),
		maxSessions: maxSessions,
	}
	go sm.cleanupLoop()
	return sm
}

func (sm *SessionManager) CreateSession(containerID string, hostPort int) (*Session, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if len(sm.sessions) >= sm.maxSessions {
		return nil, fmt.Errorf("maximum active sessions reached")
	}
	// Check for port collision
	for _, sess := range sm.sessions {
		if sess.HostPort == hostPort {
			return nil, fmt.Errorf("port collision detected")
		}
	}
	id := uuid.New().String()
	now := time.Now()
	s := &Session{
		SessionID:   id,
		ContainerID: containerID,
		HostPort:    hostPort,
		CreatedAt:   now,
		LastActive:  now,
	}
	sm.sessions[id] = s
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
	if s, ok := sm.sessions[sessionID]; ok {
		go func(containerID string) {
			ctx := context.Background()
			stopTimeout := 10
			if err := sm.docker.cli.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &stopTimeout}); err != nil {
				fmt.Printf("[CLEANUP] Failed to stop container %s: %v\n", containerID, err)
			} else {
				fmt.Printf("[CLEANUP] Stopped container %s for session %s\n", containerID, sessionID)
			}
		}(s.ContainerID)
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

// FindAvailablePort returns a random available port in the range
func (sm *SessionManager) FindAvailablePort() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	used := make(map[int]bool)
	for _, s := range sm.sessions {
		used[s.HostPort] = true
	}
	for tries := 0; tries < 1000; tries++ {
		port := rand.Intn(10000) + 20000
		if !used[port] {
			return port
		}
	}
	return 0 // no available port
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
		if totalAge > 30*time.Minute || idleAge > 5*time.Minute {
			go func(sessionID, containerID string) {
				ctx := context.Background()
			stopTimeout := 10
			if err := sm.docker.cli.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &stopTimeout}); err != nil {
					fmt.Printf("[CLEANUP] Failed to stop container %s: %v\n", containerID, err)
				} else {
					fmt.Printf("[CLEANUP] Stopped container %s for session %s (total: %v, idle: %v)\n", containerID, sessionID, totalAge, idleAge)
				}
			}(id, s.ContainerID)
			delete(sm.sessions, id)
		}
	}
}
