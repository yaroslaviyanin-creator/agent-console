package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"agent-console/server/internal/provider"
	"agent-console/server/internal/store"
	"github.com/gorilla/websocket"
)

// Direct RunRequest and RunEvent frames use the same field names as src/protocol/types.ts.
type runRequest struct {
	ChatID    string `json:"chatId"`
	MessageID string `json:"messageId"`
	Prompt    string `json:"prompt"`
	Model     struct {
		Provider string `json:"provider"`
		ID       string `json:"id"`
	} `json:"model"`
	Target struct {
		Mode string `json:"mode"`
	} `json:"target"`
	RetryOf string `json:"retryOf"`
}

type runEvent struct {
	ChatID     string `json:"chatId"`
	MessageID  string `json:"messageId"`
	Seq        int    `json:"seq"`
	Type       string `json:"type"`
	Delta      string `json:"delta,omitempty"`
	Reason     string `json:"reason,omitempty"`
	DurationMs *int64 `json:"durationMs,omitempty"`
	Code       string `json:"code,omitempty"`
	Message    string `json:"message,omitempty"`
	Retryable  *bool  `json:"retryable,omitempty"`
}

const (
	connectionTimeout = 60 * time.Second
	writeTimeout      = 5 * time.Second
)

var correlationID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
var modelID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]{0,127}$`)

type liveRuns struct {
	ctx     context.Context
	backend provider.Provider
	mu      sync.Mutex
	clients map[string]map[*socket]bool
	chats   map[string]*sync.Mutex
	slots   chan struct{}
}

type socket struct {
	conn   *websocket.Conn
	events chan runEvent
}

func newLiveRuns(ctx context.Context, backend provider.Provider) *liveRuns {
	return &liveRuns{
		ctx: ctx, backend: backend, clients: make(map[string]map[*socket]bool),
		chats: make(map[string]*sync.Mutex), slots: make(chan struct{}, 32),
	}
}

func (a *API) websocket(w http.ResponseWriter, r *http.Request) {
	s, err := a.authenticate(r.Context(), r.URL.Query().Get("token"))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		unauthorized(w)
		return
	case err != nil:
		a.internal(w, r, err)
		return
	}

	upgrader := websocket.Upgrader{
		HandshakeTimeout: writeTimeout,
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			return origin == "" || a.config.CORSOrigin == "*" || a.config.CORSOrigin == origin
		},
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	client := &socket{conn: conn, events: make(chan runEvent, 128)}
	a.live.add(s.User.ID, client)
	defer a.live.remove(s.User.ID, client)
	go client.write(a.live.ctx)
	conn.SetReadLimit(128 << 10)
	_ = conn.SetReadDeadline(time.Now().Add(connectionTimeout))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(connectionTimeout)) })

	for {
		kind, payload, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var req runRequest
		if kind != websocket.TextMessage || json.Unmarshal(payload, &req) != nil ||
			!correlationID.MatchString(req.ChatID) || !correlationID.MatchString(req.MessageID) {
			_ = conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "Invalid RunRequest"), time.Now().Add(writeTimeout))
			return
		}
		a.submit(s.User.ID, client, req)
	}
}

func (s *socket) write(ctx context.Context) {
	defer s.conn.Close()
	pings := time.NewTicker(connectionTimeout / 2)
	defer pings.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-pings.C:
			if s.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeTimeout)) != nil {
				return
			}
		case event, ok := <-s.events:
			if !ok || s.conn.SetWriteDeadline(time.Now().Add(writeTimeout)) != nil || s.conn.WriteJSON(event) != nil {
				return
			}
		}
	}
}

func (r *liveRuns) add(userID string, c *socket) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.clients[userID] == nil {
		r.clients[userID] = make(map[*socket]bool)
	}
	r.clients[userID][c] = true
}

func (r *liveRuns) remove(userID string, c *socket) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.clients[userID], c)
	if len(r.clients[userID]) == 0 {
		delete(r.clients, userID)
	}
	close(c.events)
}

// A slow consumer reconnects and reloads durable history. It cannot delay the others.
func enqueue(c *socket, event runEvent) {
	select {
	case c.events <- event:
	default:
		_ = c.conn.Close()
	}
}

func (r *liveRuns) publish(userID string, event runEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for client := range r.clients[userID] {
		enqueue(client, event)
	}
}

func (r *liveRuns) acquire(userID, chatID string) (func(*runEvent), string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	lock := r.chats[chatID]
	if lock == nil {
		lock = &sync.Mutex{}
	}
	if !lock.TryLock() {
		return nil, "chat_busy"
	}
	select {
	case r.slots <- struct{}{}:
	default:
		lock.Unlock()
		return nil, "server_busy"
	}
	r.chats[chatID] = lock
	return func(terminal *runEvent) {
		r.mu.Lock()
		defer r.mu.Unlock()
		delete(r.chats, chatID)
		lock.Unlock()
		<-r.slots
		if terminal != nil {
			for client := range r.clients[userID] {
				enqueue(client, *terminal)
			}
		}
	}, ""
}

func (a *API) submit(userID string, client *socket, req runRequest) {
	reject := func(code, message string, retryable bool) {
		enqueue(client, req.failure(1, code, message, retryable))
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Target.Mode != "chat" || req.RetryOf != "" || len(req.Prompt) > 32<<10 || req.Prompt == "" ||
		strings.ContainsRune(req.Prompt, 0) || !modelID.MatchString(req.Model.ID) {
		reject("invalid_request", "Send a message in chat mode with a valid model.", false)
		return
	}
	// The stub accepts any model family supported by the client protocol.
	switch req.Model.Provider {
	case "claude", "codex", "paid_api":
	default:
		reject("unsupported_model", "Unknown model provider.", false)
		return
	}
	if a.config.Provider == "codex" && req.Model.Provider != "codex" {
		reject("unsupported_model", "Select a Codex model for this server.", false)
		return
	}
	// Check ownership before taking a chat lock so chat_busy does not reveal another user's chat.
	if err := a.db.CheckChat(a.live.ctx, userID, req.ChatID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			reject("chat_not_found", "Chat not found.", false)
		} else {
			reject("storage_error", "Could not open the chat.", true)
		}
		return
	}
	release, code := a.live.acquire(userID, req.ChatID)
	if release == nil {
		reject(code, "Another response is running. Try again later.", true)
		return
	}
	if err := a.startRun(userID, req, release); err != nil {
		release(nil)
		switch {
		case errors.Is(err, store.ErrDuplicateRun):
			reject("duplicate_run", "This message ID has already been used.", false)
		default:
			a.logger.Error("save user message", "chatId", req.ChatID, "error", err)
			reject("storage_error", "Could not save the message.", true)
		}
	}
}

func (a *API) startRun(userID string, req runRequest, release func(*runEvent)) error {
	ctx, cancel := context.WithTimeout(a.live.ctx, 5*time.Minute)
	sessionID, err := a.db.BeginRun(ctx, userID, req.ChatID, req.MessageID, req.Prompt, a.config.Provider, req.Model.ID)
	if err != nil {
		cancel()
		return err
	}
	go func() {
		defer cancel()
		terminal := a.streamRun(ctx, userID, sessionID, req)
		release(&terminal)
	}()
	return nil
}

func (a *API) streamRun(ctx context.Context, userID, sessionID string, req runRequest) runEvent {
	seq := 0
	failure := func(code, message string, err error) runEvent {
		a.logger.Error("stream response", "chatId", req.ChatID, "code", code, "error", err)
		return req.failure(seq+1, code, message, true)
	}
	started := time.Now()
	events, err := a.live.backend.Stream(ctx, provider.Request{UserID: userID, Model: req.Model.ID, Prompt: req.Prompt, SessionID: sessionID})
	if err != nil {
		return failure("provider_error", "Could not start the provider.", err)
	}
	var text, thinking bytes.Buffer
	for {
		select {
		case <-ctx.Done():
			return failure("run_interrupted", "The response was interrupted.", ctx.Err())
		case event, ok := <-events:
			if !ok {
				return failure("incomplete_response", "Provider did not complete the response.", nil)
			}
			wire := runEvent{ChatID: req.ChatID, MessageID: req.MessageID, Seq: seq + 1, Type: event.Type}
			switch event.Type {
			case provider.Text, provider.Thinking:
				if event.Delta == "" {
					continue
				}
				if text.Len()+thinking.Len()+len(event.Delta) > 2<<20 {
					return failure("response_too_large", "Provider response exceeded the size limit.", nil)
				}
				if event.Type == provider.Text {
					text.WriteString(event.Delta)
				} else {
					thinking.WriteString(event.Delta)
				}
				wire.Delta = event.Delta
			case provider.Done:
				durationMs := time.Since(started).Milliseconds()
				if err := a.db.FinishRun(ctx, req.ChatID, req.MessageID, text.String(), thinking.String(), event.SessionID, durationMs); err != nil {
					return failure("storage_error", "Could not save the response.", err)
				}
				wire.Reason = "completed"
				wire.DurationMs = &durationMs
			case provider.Error:
				return failure("provider_error", "The provider could not complete the response.", event.Err)
			default:
				continue
			}
			seq++
			if event.Type == provider.Done {
				return wire
			}
			a.live.publish(userID, wire)
		}
	}
}

func (req runRequest) failure(seq int, code, message string, retryable bool) runEvent {
	return runEvent{ChatID: req.ChatID, MessageID: req.MessageID, Seq: seq, Type: "error", Code: code, Message: message, Retryable: &retryable}
}
