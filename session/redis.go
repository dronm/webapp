// Package session contains net/http session helpers.
package session

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	core "github.com/dronm/session"
	_ "github.com/dronm/session/redis"
)

const DefaultCookieName = "_s"

type RedisConfig struct {
	Connect   string
	Namespace string
}

type Config struct {
	CookieName     string
	CookieDomain   string
	Secure         bool
	HTTPOnly       bool
	SameSite       http.SameSite
	MaxLifeTime    int64
	MaxIdleTime    int64
	DestroyAllTime string
}

type Manager struct {
	manager *core.Manager
	config  Config
}

func NewRedisManager(cfg Config, redisCfg RedisConfig) (*Manager, error) {
	manager, err := core.NewManager(
		"redis",
		cfg.MaxLifeTime,
		cfg.MaxIdleTime,
		cfg.DestroyAllTime,
		redisCfg.Connect,
		redisCfg.Namespace,
	)
	if err != nil {
		return nil, fmt.Errorf("session.NewManager(): %w", err)
	}

	if cfg.CookieName == "" {
		cfg.CookieName = DefaultCookieName
	}
	if !cfg.HTTPOnly {
		cfg.HTTPOnly = true
	}
	if cfg.SameSite == 0 {
		cfg.SameSite = http.SameSiteLaxMode
	}

	return &Manager{
		manager: manager,
		config:  cfg,
	}, nil
}

func (m *Manager) Core() *core.Manager {
	if m == nil {
		return nil
	}

	return m.manager
}

func (m *Manager) StartGC(writer io.Writer, level string) {
	if m == nil || m.manager == nil {
		return
	}

	var logLevel core.LogLevel
	switch strings.ToLower(level) {
	case "debug":
		logLevel = core.LOG_LEVEL_DEBUG
	case "warn", "warning":
		logLevel = core.LOG_LEVEL_WARN
	default:
		logLevel = core.LOG_LEVEL_ERROR
	}

	m.manager.StartGC(writer, logLevel)
}

func (m *Manager) Resolver(w http.ResponseWriter, r *http.Request) (core.Session, error) {
	if m == nil || m.manager == nil {
		return nil, fmt.Errorf("session manager is nil")
	}

	sessionID := m.extractSessionID(r)
	sessionAlreadyExists := sessionID != ""

	sess, err := m.manager.SessionStart(sessionID)
	if err != nil {
		return nil, fmt.Errorf("SessionStart(): %w", err)
	}

	if !sessionAlreadyExists {
		_ = sess.Put("time_created", time.Now())
	}

	if w != nil {
		maxAge := int(m.config.MaxLifeTime)
		http.SetCookie(w, &http.Cookie{
			Name:     m.config.CookieName,
			Value:    sess.SessionID(),
			Path:     "/",
			Domain:   m.config.CookieDomain,
			MaxAge:   maxAge,
			Secure:   m.config.Secure,
			HttpOnly: m.config.HTTPOnly,
			SameSite: m.config.SameSite,
		})
	}

	return sess, nil
}

func (m *Manager) extractSessionID(r *http.Request) string {
	if r == nil {
		return ""
	}

	if cookie, err := r.Cookie(m.config.CookieName); err == nil {
		return cookie.Value
	}

	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return ""
	}

	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 {
		return ""
	}

	if strings.ToLower(parts[0]) != "bearer" {
		return ""
	}

	return strings.TrimSpace(parts[1])
}
