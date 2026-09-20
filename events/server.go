// Package events provides PostgreSQL LISTEN/NOTIFY integration.
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dronm/webapp"
)

const (
	defaultAcquireWait       = 100 * time.Millisecond
	defaultMaxAcquireWait    = 60 * time.Second
	defaultNotificationWait  = 500 * time.Millisecond
	defaultLocalCallTimeout  = time.Minute
	defaultCommandBufferSize = 32
)

type Publisher interface {
	PublishEvent(publisherID string, eventID string, payload any) error
}

type Target struct {
	Service string
	Method  string
}

type Config struct {
	Pool          *pgxpool.Pool
	AppContext    *webapp.AppContext
	Publisher     Publisher
	LocalEvents   map[string]Target
	CallTimeout   time.Duration
	WaitTimeout   time.Duration
	CommandBuffer int
}

type Server struct {
	pool        *pgxpool.Pool
	appContext  *webapp.AppContext
	publisher   Publisher
	localEvents map[string]Target

	events *UniqEvents
	cmdCh  chan string

	callTimeout time.Duration
	waitTimeout time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

func NewServer(cfg Config) *Server {
	callTimeout := cfg.CallTimeout
	if callTimeout <= 0 {
		callTimeout = defaultLocalCallTimeout
	}

	waitTimeout := cfg.WaitTimeout
	if waitTimeout <= 0 {
		waitTimeout = defaultNotificationWait
	}

	commandBuffer := cfg.CommandBuffer
	if commandBuffer <= 0 {
		commandBuffer = defaultCommandBufferSize
	}

	events := NewUniqEvents()
	for eventID := range cfg.LocalEvents {
		events.forceAdd(eventID)
	}

	return &Server{
		pool:        cfg.Pool,
		appContext:  cfg.AppContext,
		publisher:   cfg.Publisher,
		localEvents: cloneTargets(cfg.LocalEvents),
		events:      events,
		cmdCh:       make(chan string, commandBuffer),
		callTimeout: callTimeout,
		waitTimeout: waitTimeout,
		done:        make(chan struct{}),
	}
}

func (s *Server) SetPublisher(publisher Publisher) {
	s.publisher = publisher
}

func (s *Server) AddEvent(eventID string) {
	if eventID == "" || s == nil || s.events == nil {
		return
	}

	if s.events.Add(eventID) {
		s.enqueueCommand(`LISTEN ` + pgx.Identifier{eventID}.Sanitize())
	}
}

func (s *Server) RemoveEvent(eventID string) {
	if eventID == "" || s == nil || s.events == nil {
		return
	}

	if s.events.Remove(eventID) {
		s.enqueueCommand(`UNLISTEN ` + pgx.Identifier{eventID}.Sanitize())
	}
}

func (s *Server) EventCount(eventID string) (int, bool) {
	if s == nil || s.events == nil {
		return 0, false
	}

	return s.events.EventCount(eventID)
}

func (s *Server) TotalEventCount() int {
	if s == nil || s.events == nil {
		return 0
	}

	return s.events.TotalEventCount()
}

func (s *Server) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}

	s.ctx, s.cancel = context.WithCancel(ctx)
	go s.serve()
}

func (s *Server) Serve(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}

	s.ctx, s.cancel = context.WithCancel(ctx)
	s.serve()
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil || s.cancel == nil {
		return nil
	}

	s.cancel()

	if ctx == nil {
		ctx = context.Background()
	}

	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) serve() {
	defer close(s.done)

	if s.pool == nil {
		slog.Error("event server pool is nil")
		return
	}

	acquireWait := defaultAcquireWait
	for {
		if s.ctx.Err() != nil {
			return
		}

		conn, err := s.pool.Acquire(s.ctx)
		if err != nil {
			slog.Error("event server acquire connection failed", "error", err)
			if !sleepContext(s.ctx, acquireWait) {
				return
			}
			acquireWait *= 2
			if acquireWait > defaultMaxAcquireWait {
				acquireWait = defaultMaxAcquireWait
			}
			continue
		}

		acquireWait = defaultAcquireWait
		if err := s.listenCurrentEvents(s.ctx, conn); err != nil {
			conn.Release()
			slog.Error("event server initial listen failed", "error", err)
			continue
		}

		err = s.runConnection(conn)
		conn.Release()
		if s.ctx.Err() != nil {
			return
		}
		if err != nil {
			slog.Error("event server connection loop failed", "error", err)
		}
	}
}

func (s *Server) runConnection(conn *pgxpool.Conn) error {
	for {
		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		case command := <-s.cmdCh:
			if _, err := conn.Exec(s.ctx, command); err != nil {
				return fmt.Errorf("exec command %q: %w", command, err)
			}
		default:
		}

		waitCtx, cancel := context.WithTimeout(s.ctx, s.waitTimeout)
		notification, err := conn.Conn().WaitForNotification(waitCtx)
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || waitCtx.Err() == context.DeadlineExceeded {
				continue
			}
			if s.ctx.Err() != nil {
				return s.ctx.Err()
			}
			return fmt.Errorf("WaitForNotification(): %w", err)
		}

		s.OnNotification(s.ctx, notification)
	}
}

func (s *Server) listenCurrentEvents(ctx context.Context, conn *pgxpool.Conn) error {
	for _, eventID := range s.events.EventIDs() {
		command := `LISTEN ` + pgx.Identifier{eventID}.Sanitize()
		if _, err := conn.Exec(ctx, command); err != nil {
			return fmt.Errorf("%s: %w", command, err)
		}
	}

	return nil
}

func (s *Server) OnNotification(ctx context.Context, notification *pgconn.Notification) {
	if notification == nil {
		return
	}

	raw := json.RawMessage([]byte(notification.Payload))
	if len(strings.TrimSpace(notification.Payload)) == 0 {
		raw = json.RawMessage("null")
	}

	if target, ok := s.localTarget(notification.Channel); ok {
		if err := s.callLocalEvent(ctx, notification.Channel, target, raw); err != nil {
			slog.Error(
				"event server local call failed",
				"channel", notification.Channel,
				"service", target.Service,
				"method", target.Method,
				"error", err,
			)
		}
		return
	}

	if s.publisher == nil {
		slog.Warn("event server publisher is nil", "channel", notification.Channel)
		return
	}

	if err := s.publisher.PublishEvent("", notification.Channel, raw); err != nil {
		slog.Error("event server publish failed", "channel", notification.Channel, "error", err)
	}
}

func (s *Server) callLocalEvent(ctx context.Context, channel string, target Target, raw json.RawMessage) error {
	if s.appContext == nil {
		return fmt.Errorf("app context is nil")
	}

	if target.Service == "" || target.Method == "" {
		parts := strings.Split(channel, ".")
		if len(parts) != 2 {
			return fmt.Errorf("invalid local event channel %q", channel)
		}
		if target.Service == "" {
			target.Service = parts[0]
		}
		if target.Method == "" {
			target.Method = parts[1]
		}
	}

	callCtx, cancel := context.WithTimeout(ctx, s.callTimeout)
	defer cancel()
	if s.appContext.Access != nil {
		callCtx = webapp.ContextWithAccess(callCtx, s.appContext.Access)
	}

	_, err := webapp.CallServiceJSON(
		callCtx,
		target.Service,
		target.Method,
		raw,
		webapp.ServiceContext{
			Access:         s.appContext.Access,
			DB:             s.appContext.DB,
			EventPublisher: s.appContext.EventPublisher,
			Session:        nil,
			QueryID:        "pg-notify",
		},
	)
	if err != nil {
		return fmt.Errorf("CallServiceMethod(): %w", err)
	}

	return nil
}

func (s *Server) localTarget(channel string) (Target, bool) {
	if s == nil || s.localEvents == nil {
		return Target{}, false
	}

	target, ok := s.localEvents[channel]
	return target, ok
}

func (s *Server) enqueueCommand(command string) {
	if s == nil || s.cmdCh == nil || command == "" {
		return
	}

	select {
	case s.cmdCh <- command:
	default:
		go func() {
			if s.ctx == nil {
				s.cmdCh <- command
				return
			}

			select {
			case s.cmdCh <- command:
			case <-s.ctx.Done():
			}
		}()
	}
}

func cloneTargets(value map[string]Target) map[string]Target {
	if len(value) == 0 {
		return nil
	}

	out := make(map[string]Target, len(value))
	for k, v := range value {
		out[k] = v
	}

	return out
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
