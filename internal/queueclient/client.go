// Package queueclient supplies single-attempt HTTP operations; the streamer owns retry policy.
package queueclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gpu-telemetry/internal/queue"
	"gpu-telemetry/internal/telemetry"
)

type Error struct {
	Status    int
	Message   string
	Temporary bool
}

func (e *Error) Error() string { return fmt.Sprintf("queue HTTP %d: %s", e.Status, e.Message) }
func Retryable(err error) bool { var e *Error; return errors.As(err, &e) && e.Temporary }

type Client struct {
	base string
	http *http.Client
}

func New(base string, timeout time.Duration) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || timeout <= 0 {
		return nil, errors.New("queue URL must be an http(s) origin without credentials, path, query or fragment; timeout must be positive")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxConnsPerHost = 2
	transport.MaxIdleConnsPerHost = 2
	return &Client{strings.TrimRight(base, "/"), &http.Client{Timeout: timeout, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) Close() { c.http.CloseIdleConnections() }

func (c *Client) request(ctx context.Context, path string, payload []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, ctx.Err()
		}
		return 0, nil, &Error{Message: err.Error(), Temporary: true}
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, queue.MaxRequestBytes+1))
	if err != nil {
		return 0, nil, &Error{Message: err.Error(), Temporary: true}
	}
	if len(b) > queue.MaxRequestBytes {
		return 0, nil, &Error{Message: "response too large", Temporary: false}
	}
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return 0, nil, &Error{resp.StatusCode, string(b), resp.StatusCode == 408 || resp.StatusCode == 429 || resp.StatusCode >= 500}
	}
	return resp.StatusCode, b, nil
}

func (c *Client) Publish(ctx context.Context, e telemetry.Event) error {
	b, err := json.Marshal(queue.PublishRequest{Events: []telemetry.Event{e}})
	if err != nil {
		return err
	}
	if len(b) > queue.MaxRequestBytes {
		return &Error{Message: "event request exceeds 64 KiB"}
	}
	status, b, err := c.request(ctx, "/internal/v1/messages", b)
	if err != nil {
		return err
	}
	var result queue.PublishResponse
	if status != 202 || json.Unmarshal(b, &result) != nil || len(result.AcceptedIDs) != 1 || result.AcceptedIDs[0] != e.EventID {
		return &Error{Status: status, Message: "unconfirmed acceptance response", Temporary: true}
	}
	return nil
}
func (c *Client) Receive(ctx context.Context) (telemetry.Event, bool, error) {
	status, b, err := c.request(ctx, "/internal/v1/receive", nil)
	if err != nil {
		return telemetry.Event{}, false, err
	}
	if status == 204 {
		return telemetry.Event{}, false, nil
	}
	var result queue.ReceiveResponse
	if status != 200 || json.Unmarshal(b, &result) != nil || len(result.Events) != 1 {
		return telemetry.Event{}, false, errors.New("invalid receive response; destructively received event may be lost")
	}
	e := result.Events[0]
	if err := e.Validate(); err != nil {
		return e, false, err
	}
	return e, true, nil
}

// Lease waits at most wait; the HTTP timeout must exceed it. Lost responses
// leave a broker-owned lease that expires; callers may retry the request.
func (c *Client) Lease(ctx context.Context, wait time.Duration) (queue.Delivery, bool, error) {
	if wait < 0 || wait > queue.MaxWait || wait >= c.http.Timeout {
		return queue.Delivery{}, false, errors.New("lease wait must be 0..10s and shorter than HTTP timeout")
	}
	return c.lease(ctx, wait, true)
}

// LeaseForProcessing preserves semantic-invalid events so a collector can quarantine them.
// Malformed JSON or missing receipt identity cannot be safely acknowledged.
func (c *Client) LeaseForProcessing(ctx context.Context, wait time.Duration) (queue.Delivery, bool, error) {
	if wait < 0 || wait > queue.MaxWait || wait >= c.http.Timeout {
		return queue.Delivery{}, false, errors.New("invalid lease wait")
	}
	return c.lease(ctx, wait, false)
}
func (c *Client) lease(ctx context.Context, wait time.Duration, validate bool) (queue.Delivery, bool, error) {
	body, _ := json.Marshal(queue.LeaseRequest{WaitMillis: wait.Milliseconds()})
	status, b, err := c.request(ctx, "/internal/v1/leases", body)
	if err != nil {
		return queue.Delivery{}, false, err
	}
	if status == 204 {
		return queue.Delivery{}, false, nil
	}
	var delivery queue.Delivery
	if status != 200 || json.Unmarshal(b, &delivery) != nil || (validate && delivery.Event.Validate() != nil) || delivery.Event.EventID == "" || len(delivery.Event.EventID) > 128 || len(delivery.Token) != 32 || delivery.ExpiresAt.IsZero() || delivery.Attempt == 0 {
		return queue.Delivery{}, false, &Error{Status: status, Message: "invalid lease response; any issued lease will expire", Temporary: true}
	}
	return delivery, true, nil
}
func (c *Client) receipt(ctx context.Context, path, id string, req any) error {
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	status, b, err := c.request(ctx, path, b)
	if err != nil {
		return err
	}
	var response queue.ReceiptResponse
	if status != 200 || json.Unmarshal(b, &response) != nil || response.EventID != id {
		return &Error{Status: status, Message: "unconfirmed receipt; retry same token", Temporary: true}
	}
	return nil
}
func (c *Client) Ack(ctx context.Context, req queue.AckRequest) error {
	return c.receipt(ctx, "/internal/v1/acks", req.EventID, req)
}
func (c *Client) Nack(ctx context.Context, req queue.NackRequest) error {
	return c.receipt(ctx, "/internal/v1/nacks", req.EventID, req)
}
