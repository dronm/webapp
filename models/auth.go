package models

import "time"

type Auth struct {
	Token        string    `json:"token"`
	TokenRefresh string    `json:"token_refresh"`
	Expires      time.Time `json:"expires"`
}
