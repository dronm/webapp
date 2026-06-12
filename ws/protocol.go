package ws

import (
	"encoding/json"

	"github.com/dronm/webapp"
)

type ClientMessage struct {
	Func    string          `json:"f"`
	QueryID string          `json:"q"`
	Payload json.RawMessage `json:"p"`
}

type ServerResponse struct {
	EventID string            `json:"event_id"`
	QueryID string            `json:"query_id"`
	Payload any               `json:"payload,omitempty"`
	Error   *webapp.ErrorBody `json:"error,omitempty"`
}
