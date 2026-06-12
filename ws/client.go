package ws

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type Client struct {
	ID       string
	Conn     *websocket.Conn
	EventBus EventBus

	writeMu   sync.Mutex
	mu        sync.Mutex
	VisitedAt time.Time
	events    map[string]struct{}
}

func NewClient(id string, conn *websocket.Conn, eventBus EventBus) *Client {
	return &Client{
		ID:       id,
		Conn:     conn,
		EventBus: eventBus,
		events:   map[string]struct{}{},
	}
}

func (c *Client) AddEvent(eventID string) {
	if c == nil || c.EventBus == nil || eventID == "" {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.events[eventID]; ok {
		return
	}

	c.EventBus.AddEvent(eventID)
	c.events[eventID] = struct{}{}
}

func (c *Client) RemoveEvent(eventID string) {
	if c == nil || c.EventBus == nil || eventID == "" {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.events[eventID]; !ok {
		return
	}

	delete(c.events, eventID)
	c.EventBus.RemoveEvent(eventID)
}

func (c *Client) RemoveAllEvents() {
	if c == nil || c.EventBus == nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for eventID := range c.events {
		c.EventBus.RemoveEvent(eventID)
	}
	c.events = map[string]struct{}{}
}

func (c *Client) HasEvent(eventID string) bool {
	if c == nil {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	_, ok := c.events[eventID]
	return ok
}

func (c *Client) Touch() {
	if c == nil {
		return
	}

	c.mu.Lock()
	c.VisitedAt = time.Now()
	c.mu.Unlock()
}
