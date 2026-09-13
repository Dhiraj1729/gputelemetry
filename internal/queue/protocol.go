package queue

import (
	"gpu-telemetry/internal/telemetry"
	"time"
)

const MaxRequestBytes = 64 << 10

type PublishRequest struct {
	Events []telemetry.Event `json:"events"`
}
type PublishResponse struct {
	AcceptedIDs []string `json:"accepted_ids"`
	Duplicate   bool     `json:"duplicate"`
}
type ReceiveResponse struct {
	Events []telemetry.Event `json:"events"`
}
type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Delivery identifies one broker-timed attempt, not ownership of future attempts.
type Delivery struct {
	Event     telemetry.Event `json:"event"`
	Token     string          `json:"token"`
	ExpiresAt time.Time       `json:"expires_at"`
	Attempt   uint64          `json:"attempt"`
}
type LeaseRequest struct {
	WaitMillis int64 `json:"wait_ms"`
}
type AckRequest struct {
	EventID string `json:"event_id"`
	Token   string `json:"token"`
}
type NackRequest struct {
	EventID     string `json:"event_id"`
	Token       string `json:"token"`
	Permanent   bool   `json:"permanent"`
	DelayMillis int64  `json:"delay_ms"`
	Reason      string `json:"reason"`
}
type ReceiptResponse struct {
	EventID   string `json:"event_id"`
	Duplicate bool   `json:"duplicate"`
}
