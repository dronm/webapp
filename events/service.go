package events

import (
	"context"
	"fmt"

	"github.com/dronm/session"

	"github.com/dronm/webapp"
)

type Handler interface {
	SubscribeToEvent(sessionID string, eventID string) error
	UnsubscribeFromEvent(sessionID string, eventID string) error
	PublishEvent(publisherID string, eventID string, payload any) error
}

type Service struct {
	Handler Handler
	Session session.Session
}

type SubscribeRequest struct {
	Events []string `json:"events"`
}

type UnsubscribeRequest struct {
	Events []string `json:"events"`
}

func NewService(ctx webapp.ServiceContext, handler Handler) *Service {
	return &Service{
		Handler: handler,
		Session: ctx.Session,
	}
}

func (s *Service) Subscribe(ctx context.Context, input SubscribeRequest) error {
	if s.Handler == nil {
		return fmt.Errorf("event handler is nil")
	}
	if s.Session == nil {
		return webapp.Unauthorized("session is required", nil)
	}

	sessionID := s.Session.SessionID()
	for _, eventID := range input.Events {
		if err := s.Handler.SubscribeToEvent(sessionID, eventID); err != nil {
			return err
		}
	}

	return nil
}

func (s *Service) Unsubscribe(ctx context.Context, input UnsubscribeRequest) error {
	if s.Handler == nil {
		return fmt.Errorf("event handler is nil")
	}
	if s.Session == nil {
		return webapp.Unauthorized("session is required", nil)
	}

	sessionID := s.Session.SessionID()
	for _, eventID := range input.Events {
		if err := s.Handler.UnsubscribeFromEvent(sessionID, eventID); err != nil {
			return err
		}
	}

	return nil
}
