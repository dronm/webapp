// Package ws provides a net/http WebSocket service bridge.
package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dronm/modelbind"
	"github.com/dronm/session"
	"github.com/gorilla/websocket"

	"github.com/dronm/webapp"
)

const (
	defaultMaxMethodCallDuration = time.Minute
	defaultWriteWait             = 10 * time.Second
	defaultPongWait              = 60 * time.Second
	defaultPingInterval          = 54 * time.Second
	defaultReadLimit             = 1024 * 1024
)

type EventBus interface {
	AddEvent(eventID string)
	RemoveEvent(eventID string)
}

type PermissionChecker func(sess session.Session, method string) error

type Config struct {
	AppContext            *webapp.AppContext
	SessionResolver       webapp.SessionResolver
	EventBus              EventBus
	CheckOrigin           func(origin string) bool
	IsMethodAllowed       PermissionChecker
	MaxMethodCallDuration time.Duration
	WriteWait             time.Duration
	PongWait              time.Duration
	PingInterval          time.Duration
	ReadLimit             int64
}

type Server struct {
	config Config

	clientsMu sync.RWMutex
	clients   map[string][]*Client
}

func NewServer(cfg Config) *Server {
	if cfg.AppContext == nil {
		cfg.AppContext = &webapp.AppContext{}
	}
	if cfg.MaxMethodCallDuration <= 0 {
		cfg.MaxMethodCallDuration = defaultMaxMethodCallDuration
	}
	if cfg.WriteWait <= 0 {
		cfg.WriteWait = defaultWriteWait
	}
	if cfg.PongWait <= 0 {
		cfg.PongWait = defaultPongWait
	}
	if cfg.PingInterval <= 0 {
		cfg.PingInterval = defaultPingInterval
	}
	if cfg.ReadLimit <= 0 {
		cfg.ReadLimit = defaultReadLimit
	}

	return &Server{
		config:  cfg,
		clients: map[string][]*Client{},
	}
}

func (s *Server) Handler() http.Handler {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if s.config.CheckOrigin == nil {
				return true
			}
			return s.config.CheckOrigin(origin)
		},
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := webapp.SessionFromContext(r.Context())
		if sess == nil && s.config.SessionResolver != nil {
			resolved, err := s.config.SessionResolver(w, r)
			if err != nil {
				webapp.WriteServiceError(w, err)
				return
			}
			sess = resolved
		}
		if sess == nil {
			webapp.WriteServiceError(w, webapp.Unauthorized("session is required", nil))
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			slog.Error("websocket upgrade failed", "error", err)
			return
		}

		client := NewClient(sess.SessionID(), conn, s.config.EventBus)
		s.addClient(client)
		defer s.removeClient(client.ID, client)

		s.handleClient(r.Context(), client, sess)
	})
}

func (s *Server) handleClient(ctx context.Context, client *Client, sess session.Session) {
	conn := client.Conn
	conn.SetReadLimit(s.config.ReadLimit)
	_ = conn.SetReadDeadline(time.Now().Add(s.config.PongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(s.config.PongWait))
	})

	pingDone := make(chan struct{})
	go s.pingLoop(client, pingDone)
	defer close(pingDone)

	for {
		messageType, message, err := conn.ReadMessage()
		if err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				slog.Debug("websocket read failed", "client_id", client.ID, "error", err)
			}
			return
		}
		if messageType != websocket.TextMessage {
			_ = s.SendMessage(client, ServerResponse{
				EventID: "Response",
				Error:   &webapp.ErrorBody{Code: "bad_request", Message: "Only text messages are supported"},
			})
			continue
		}

		client.Touch()
		s.handleMessage(ctx, client, sess, message)
	}
}

func (s *Server) handleMessage(ctx context.Context, client *Client, sess session.Session, message []byte) {
	var clientMsg ClientMessage
	if err := json.Unmarshal(message, &clientMsg); err != nil {
		_ = s.SendMessage(client, ServerResponse{
			EventID: "Response",
			Error:   errorBody(webapp.BadRequest("Invalid JSON message", nil)),
		})
		return
	}

	response := ServerResponse{
		EventID: "Response",
		QueryID: clientMsg.QueryID,
	}

	serviceName, methodName, err := splitFunc(clientMsg.Func)
	if err != nil {
		response.Error = errorBody(webapp.BadRequest(err.Error(), nil))
		_ = s.SendMessage(client, response)
		return
	}

	fullMethod := serviceName + "." + methodName
	if s.config.IsMethodAllowed != nil {
		if err := s.config.IsMethodAllowed(sess, fullMethod); err != nil {
			response.Error = errorBody(webapp.Forbidden("method is not allowed", map[string]any{"method": fullMethod}))
			_ = s.SendMessage(client, response)
			return
		}
	}

	input, err := webapp.DecodeInputForService(serviceName, methodName, clientMsg.Payload)
	if err != nil {
		response.Error = errorBody(webapp.BadRequest(err.Error(), nil))
		_ = s.SendMessage(client, response)
		return
	}

	callCtx, cancel := context.WithTimeout(ctx, s.config.MaxMethodCallDuration)
	defer cancel()

	result, err := webapp.CallServiceMethod(
		callCtx,
		serviceName,
		methodName,
		input,
		webapp.ServiceContext{
			DB:             s.config.AppContext.DB,
			EventPublisher: s.config.AppContext.EventPublisher,
			Session:        sess,
			QueryID:        clientMsg.QueryID,
		},
	)
	if err != nil {
		response.Error = errorBody(err)
		_ = s.SendMessage(client, response)
		return
	}

	response.Payload = result
	_ = s.SendMessage(client, response)
}

func (s *Server) SendMessage(client *Client, response ServerResponse) error {
	if client == nil || client.Conn == nil {
		return fmt.Errorf("client is nil")
	}

	payload, err := json.Marshal(response)
	if err != nil {
		return fmt.Errorf("json.Marshal(): %w", err)
	}

	client.writeMu.Lock()
	defer client.writeMu.Unlock()

	_ = client.Conn.SetWriteDeadline(time.Now().Add(s.config.WriteWait))
	if err := client.Conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		return fmt.Errorf("WriteMessage(): %w", err)
	}

	return nil
}

func (s *Server) PublishEvent(publisherID string, eventID string, payload any) error {
	message, err := json.Marshal(ServerResponse{
		EventID: eventID,
		Payload: payload,
	})
	if err != nil {
		return fmt.Errorf("json.Marshal(): %w", err)
	}

	targets := s.eventTargets(publisherID, eventID)
	for _, client := range targets {
		client.writeMu.Lock()
		_ = client.Conn.SetWriteDeadline(time.Now().Add(s.config.WriteWait))
		err := client.Conn.WriteMessage(websocket.TextMessage, message)
		client.writeMu.Unlock()
		if err != nil {
			go s.removeClient(client.ID, client)
		}
	}

	return nil
}

func (s *Server) SubscribeToEvent(sessionID string, eventID string) error {
	clients := s.clientsBySession(sessionID)
	if len(clients) == 0 {
		return fmt.Errorf("session not connected: %s", sessionID)
	}

	for _, client := range clients {
		client.AddEvent(eventID)
	}

	return nil
}

func (s *Server) UnsubscribeFromEvent(sessionID string, eventID string) error {
	clients := s.clientsBySession(sessionID)
	if len(clients) == 0 {
		return fmt.Errorf("session not connected: %s", sessionID)
	}

	for _, client := range clients {
		client.RemoveEvent(eventID)
	}

	return nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	clients := s.allClients()
	for _, client := range clients {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		client.writeMu.Lock()
		_ = client.Conn.SetWriteDeadline(time.Now().Add(s.config.WriteWait))
		_ = client.Conn.WriteMessage(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "server shutting down"),
		)
		client.writeMu.Unlock()
		_ = client.Conn.Close()
		client.RemoveAllEvents()
	}

	return nil
}

func (s *Server) pingLoop(client *Client, done <-chan struct{}) {
	ticker := time.NewTicker(s.config.PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			client.writeMu.Lock()
			_ = client.Conn.SetWriteDeadline(time.Now().Add(s.config.WriteWait))
			err := client.Conn.WriteMessage(websocket.PingMessage, nil)
			client.writeMu.Unlock()
			if err != nil {
				_ = client.Conn.Close()
				return
			}
		}
	}
}

func (s *Server) addClient(client *Client) {
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()

	s.clients[client.ID] = append(s.clients[client.ID], client)
}

func (s *Server) removeClient(sessionID string, target *Client) {
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()

	clients := s.clients[sessionID]
	out := clients[:0]
	for _, client := range clients {
		if client == target {
			client.RemoveAllEvents()
			_ = client.Conn.Close()
			continue
		}
		out = append(out, client)
	}

	if len(out) == 0 {
		delete(s.clients, sessionID)
		return
	}

	s.clients[sessionID] = out
}

func (s *Server) clientsBySession(sessionID string) []*Client {
	s.clientsMu.RLock()
	defer s.clientsMu.RUnlock()

	clients := s.clients[sessionID]
	out := make([]*Client, len(clients))
	copy(out, clients)
	return out
}

func (s *Server) allClients() []*Client {
	s.clientsMu.RLock()
	defer s.clientsMu.RUnlock()

	out := make([]*Client, 0)
	for _, clients := range s.clients {
		out = append(out, clients...)
	}

	return out
}

func (s *Server) eventTargets(publisherID string, eventID string) []*Client {
	s.clientsMu.RLock()
	defer s.clientsMu.RUnlock()

	out := make([]*Client, 0)
	for _, clients := range s.clients {
		for _, client := range clients {
			if client.ID == publisherID {
				continue
			}
			if client.HasEvent(eventID) {
				out = append(out, client)
			}
		}
	}

	return out
}

func splitFunc(value string) (string, string, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("func must have Service.Method format")
	}

	return parts[0], parts[1], nil
}

func errorBody(err error) *webapp.ErrorBody {
	var httpErr webapp.HTTPError
	if errors.As(err, &httpErr) {
		return &webapp.ErrorBody{
			Code:    httpErr.Code(),
			Message: httpErr.PublicMessage(),
			Details: httpErr.Details(),
		}
	}

	if webapp.IsModelBindError(err) {
		return &webapp.ErrorBody{
			Code:    "bad_request",
			Message: modelbind.TranslateError(modelbind.LanguageEN, err),
		}
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return &webapp.ErrorBody{
			Code:    "timeout",
			Message: "Request timeout",
		}
	}

	return &webapp.ErrorBody{
		Code:    "internal_error",
		Message: "Internal server error",
	}
}
