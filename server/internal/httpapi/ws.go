package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"agent-console/server/internal/provider"
	"agent-console/server/internal/store"
	"agent-console/server/internal/uploads"
	"github.com/gorilla/websocket"
)

// Direct RunRequest and RunEvent frames use the same field names as src/protocol/types.ts.
type runRequest struct {
	ChatID             string   `json:"chatId"`
	MessageID          string   `json:"messageId"`
	ReuseUserMessageID string   `json:"reuseUserMessageId,omitempty"`
	Prompt             string   `json:"prompt"`
	AttachmentIDs      []string `json:"attachmentIds,omitempty"`
	Reasoning          string   `json:"reasoning,omitempty"`
	Provider           string   `json:"provider,omitempty"`
	Model              struct {
		Provider string `json:"provider"`
		ID       string `json:"id"`
	} `json:"model"`
	Target struct {
		Mode string `json:"mode"`
	} `json:"target"`
	RetryOf string `json:"retryOf"`
}

type runEvent struct {
	ChatID      string                    `json:"chatId"`
	MessageID   string                    `json:"messageId"`
	Seq         int                       `json:"seq"`
	Type        string                    `json:"type"`
	Prompt      string                    `json:"prompt,omitempty"`
	Attachments []store.MessageAttachment `json:"attachments,omitempty"`
	Provider    string                    `json:"provider,omitempty"`
	Model       string                    `json:"model,omitempty"`
	CreatedAt   int64                     `json:"createdAt,omitempty"`
	Delta       string                    `json:"delta,omitempty"`
	Reason      string                    `json:"reason,omitempty"`
	DurationMs  *int64                    `json:"durationMs,omitempty"`
	Usage       *store.MessageUsage       `json:"usage,omitempty"`
	Code        string                    `json:"code,omitempty"`
	Message     string                    `json:"message,omitempty"`
	Retryable   *bool                     `json:"retryable,omitempty"`
}

const (
	connectionTimeout = 60 * time.Second
	writeTimeout      = 5 * time.Second
)

var correlationID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)
var modelID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]{0,127}$`)

type liveRuns struct {
	ctx               context.Context
	defaultProvider   string
	providers         map[string]provider.Provider
	db                *store.Store
	logger            *slog.Logger
	mu                sync.Mutex
	clients           map[string]map[*socket]bool
	chats             map[string]activeRun
	slots             chan struct{}
	tickets           map[string]wsTicket
	nextTicketCleanup time.Time
}

type activeRun struct {
	userID, deviceID string
	cancel           context.CancelFunc
}

type wsTicket struct {
	userID, deviceID string
	expiresAt        time.Time
}

type socket struct {
	conn     *websocket.Conn
	events   chan runEvent
	deviceID string
	revoked  bool // guarded by liveRuns.mu
}

func newLiveRuns(ctx context.Context, backend provider.Provider, concurrency int) *liveRuns {
	return newLiveRunsWithProviders(ctx, "stub", map[string]provider.Provider{"stub": backend}, concurrency)
}

func newLiveRunsWithProviders(ctx context.Context, defaultProvider string, providers map[string]provider.Provider, concurrency int) *liveRuns {
	copied := make(map[string]provider.Provider, len(providers))
	for k, v := range providers {
		if v != nil {
			copied[k] = v
		}
	}
	return &liveRuns{
		ctx: ctx, defaultProvider: defaultProvider, providers: copied,
		clients: make(map[string]map[*socket]bool),
		chats:   make(map[string]activeRun), slots: make(chan struct{}, concurrency),
		tickets: make(map[string]wsTicket),
	}
}

func (r *liveRuns) provider(id string) (provider.Provider, bool) {
	if r == nil {
		return nil, false
	}
	p, ok := r.providers[id]
	return p, ok && p != nil
}

func (r *liveRuns) hasProvider(id string) bool {
	_, ok := r.provider(id)
	return ok
}

func (a *API) issueWSTicket(w http.ResponseWriter, r *http.Request, s session) {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		a.internal(w, r, err)
		return
	}
	key := hex.EncodeToString(secret[:])
	a.live.mu.Lock()
	now := time.Now()
	if !now.Before(a.live.nextTicketCleanup) {
		for key, ticket := range a.live.tickets {
			if !now.Before(ticket.expiresAt) {
				delete(a.live.tickets, key)
			}
		}
		a.live.nextTicketCleanup = now.Add(30 * time.Second)
	}
	a.live.tickets[key] = wsTicket{s.User.ID, s.Device.ID, now.Add(30 * time.Second)}
	a.live.mu.Unlock()
	reply(w, http.StatusOK, map[string]string{"ticket": key})
}

func (r *liveRuns) takeTicket(key string) (wsTicket, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ticket, ok := r.tickets[key]
	delete(r.tickets, key)
	return ticket, ok && time.Now().Before(ticket.expiresAt)
}

func (a *API) websocket(w http.ResponseWriter, r *http.Request) {
	ticket, ok := a.live.takeTicket(r.URL.Query().Get("ticket"))
	if !ok {
		// Pending sessions cannot mint tickets. Recognize a bearer credential if supplied.
		if token := bearerToken(r); token != "" {
			if s, err := a.authenticate(r.Context(), token); err == nil && s.User.Role == "pending" {
				pendingAccount(w)
				return
			}
		}
		unauthorized(w)
		return
	}
	user, _, err := a.db.AuthenticateDevice(r.Context(), ticket.userID, ticket.deviceID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		unauthorized(w)
		return
	case errors.Is(err, store.ErrInactiveUser):
		inactive(w)
		return
	case err != nil:
		a.internal(w, r, err)
		return
	}

	if user.Role == "pending" {
		pendingAccount(w)
		return
	}

	upgrader := websocket.Upgrader{
		HandshakeTimeout: writeTimeout,
		CheckOrigin: func(r *http.Request) bool {
			return a.allowsOrigin(r.Header.Get("Origin"))
		},
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	client := &socket{conn: conn, events: make(chan runEvent, 128), deviceID: ticket.deviceID}
	a.live.add(ticket.userID, client)
	defer a.live.remove(ticket.userID, client)
	// A revocation between the handshake and registration must still take effect.
	if user, _, err := a.db.AuthenticateDevice(r.Context(), ticket.userID, ticket.deviceID); err != nil || user.Role == "pending" {
		return
	}
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
		a.submit(ticket.userID, client, req)
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

func (r *liveRuns) disconnectUser(userID string) {
	r.disconnectDevice(userID, "")
}

// An empty device ID revokes all live activity for the user.
func (r *liveRuns) disconnectDevice(userID, deviceID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	matches := func(id string) bool { return deviceID == "" || deviceID == id }
	for client := range r.clients[userID] {
		if matches(client.deviceID) {
			client.revoked = true
			_ = client.conn.Close()
		}
	}
	for _, run := range r.chats {
		if run.userID == userID && matches(run.deviceID) {
			run.cancel()
		}
	}
}

// A slow consumer reconnects and reloads durable history. It cannot delay the others.
func enqueue(c *socket, event runEvent) {
	select {
	case c.events <- event:
	default:
		_ = c.conn.Close()
	}
}

func (r *liveRuns) sendLocked(userID string, event runEvent) {
	for client := range r.clients[userID] {
		if !client.revoked {
			enqueue(client, event)
		}
	}
}

func (r *liveRuns) publish(event runEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.publishLocked(event)
}

func (r *liveRuns) publishLocked(event runEvent) {
	members, err := r.db.ChatMembers(r.ctx, event.ChatID)
	if err != nil {
		r.logger.Error("load chat members", "chatId", event.ChatID, "error", err)
		return
	}
	for _, userID := range members {
		r.sendLocked(userID, event)
	}
}

var errShareDuringRun = errors.New("cannot share private chat during a response")

func (r *liveRuns) changeShare(ctx context.Context, ownerID, chatID, targetID, access string) error {
	// Serialize changes with broadcasts so a revoked user is excluded from new frames.
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, busy := r.chats[chatID]; busy && access != "" {
		if err := r.db.CheckChat(ctx, ownerID, chatID); err != nil {
			return err
		}
		shared, err := r.db.IsChatShared(ctx, chatID)
		if err != nil {
			return err
		}
		if !shared {
			return errShareDuringRun
		}
	}
	if err := r.db.SetChatShare(ctx, ownerID, chatID, targetID, access); err != nil {
		return err
	}
	event := runEvent{Type: "workspace_changed", ChatID: chatID}
	r.sendLocked(ownerID, event)
	r.sendLocked(targetID, event)
	return nil
}

func (r *liveRuns) acquire(userID, chatID string, client *socket, cancel context.CancelFunc) (func(*runEvent), string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if client.revoked {
		return nil, "session_revoked"
	}
	if _, busy := r.chats[chatID]; busy {
		return nil, "chat_busy"
	}
	r.chats[chatID] = activeRun{userID, client.deviceID, cancel}
	return func(terminal *runEvent) {
		r.mu.Lock()
		defer r.mu.Unlock()
		delete(r.chats, chatID)
		if terminal != nil {
			r.publishLocked(*terminal)
		}
	}, ""
}

func (a *API) submit(userID string, client *socket, req runRequest) {
	reject := func(code, message string, retryable bool) {
		enqueue(client, req.failure(1, code, message, retryable))
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	req.ReuseUserMessageID = strings.TrimSpace(req.ReuseUserMessageID)
	if req.Target.Mode != "chat" || req.RetryOf != "" || len(req.Prompt) > 32<<10 ||
		(req.Prompt == "" && len(req.AttachmentIDs) == 0 && req.ReuseUserMessageID == "") ||
		len(req.AttachmentIDs) > a.config.EffectiveMaxAttachmentsPerMessage() ||
		(req.ReuseUserMessageID != "" && !correlationID.MatchString(req.ReuseUserMessageID)) ||
		strings.ContainsRune(req.Prompt, 0) || !modelID.MatchString(req.Model.ID) {
		reject("invalid_request", "Send a message in chat mode with a valid model.", false)
		return
	}
	for _, attID := range req.AttachmentIDs {
		if !correlationID.MatchString(attID) {
			reject("invalid_request", "Invalid attachment ID.", false)
			return
		}
	}
	if req.Reasoning != "" {
		switch strings.ToLower(strings.TrimSpace(req.Reasoning)) {
		case "default", "minimal", "low", "medium", "high", "xhigh", "thinking":
		default:
			reject("invalid_request", "Send a message with a valid reasoning level.", false)
			return
		}
	}
	// Resolve the workspace and check write access before provider routing or revealing busy state.
	ownerID, chatProvider, err := a.db.ChatDispatchInfo(a.live.ctx, userID, req.ChatID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			reject("chat_not_found", "Chat not found.", false)
		} else {
			reject("storage_error", "Could not open the chat.", true)
		}
		return
	}
	reqProvider := strings.ToLower(strings.TrimSpace(req.Provider))
	modelProvider := strings.ToLower(strings.TrimSpace(req.Model.Provider))
	if reqProvider == "paid_api" || modelProvider == "paid_api" {
		reject("provider_unavailable", "Paid API provider is not available.", false)
		return
	}
	targetProvider := chatProvider
	if reqProvider != "" {
		targetProvider = reqProvider
	}
	switch targetProvider {
	case "antigravity", "codex", "claude", "stub":
	default:
		reject("unsupported_model", "Unknown model provider.", false)
		return
	}
	if !a.live.hasProvider(targetProvider) {
		reject("provider_unavailable", "The selected provider is not available.", false)
		return
	}
	if modelProvider != "" {
		switch modelProvider {
		case "antigravity", "codex", "claude", "stub":
		default:
			reject("unsupported_model", "Unknown model provider.", false)
			return
		}
		if modelProvider != targetProvider && !(targetProvider == "stub" && modelProvider == "codex") {
			reject("provider_mismatch", "Selected model provider does not match this chat's provider.", false)
			return
		}
	}
	ctx, cancel := context.WithCancel(a.live.ctx)
	release, code := a.live.acquire(userID, req.ChatID, client, cancel)
	if release == nil {
		cancel()
		if code == "session_revoked" {
			return
		}
		reject(code, "Another response is running. Try again later.", true)
		return
	}
	if err := a.startRun(ctx, cancel, userID, client.deviceID, ownerID, targetProvider, req, release); err != nil {
		cancel()
		release(nil)
		var denied store.RunDenied
		switch {
		case errors.As(err, &denied):
			reject(string(denied), runDeniedMessages[denied], false)
		case errors.Is(err, sql.ErrNoRows):
			reject("chat_not_found", "Chat not found.", false)
		case errors.Is(err, store.ErrProviderUnavailable):
			reject("provider_unavailable", "The selected provider is not available.", false)
		case errors.Is(err, store.ErrProviderMismatch):
			reject("provider_mismatch", "Selected model provider does not match this chat's provider.", false)
		case errors.Is(err, store.ErrDuplicateRun):
			reject("duplicate_run", "This message ID has already been used.", false)
		case errors.Is(err, store.ErrInvalidAttachments):
			reject("invalid_attachments", "One or more attachments are invalid or unavailable.", false)
		default:
			a.logger.Error("save user message", "chatId", req.ChatID, "error", err)
			reject("storage_error", "Could not save the message.", true)
		}
	}
}

var runDeniedMessages = map[store.RunDenied]string{
	"session_revoked":         "Сессия завершена. Войдите снова.",
	"account_pending":         "Аккаунт ожидает подтверждения администратора.",
	"subscription_expired":    "Срок подписки истёк.",
	"no_active_plan":          "Для аккаунта не подключён тариф.",
	"weekly_limit_reached":    "Достигнут лимит расхода за 7 дней.",
	"five_hour_limit_reached": "Достигнут лимит расхода за 5 часов.",
}

func (a *API) startRun(ctx context.Context, cancel context.CancelFunc, userID, deviceID, ownerID, targetProvider string, req runRequest, release func(*runEvent)) error {
	_, attached, err := a.db.BeginRunForDeviceWithAttachmentsReuse(ctx, userID, deviceID, req.ChatID, req.MessageID, req.Prompt, targetProvider, req.Model.ID, req.AttachmentIDs, a.config.EffectiveMaxAttachmentsPerMessage(), req.ReuseUserMessageID)
	if err != nil {
		return err
	}
	wireAtts := make([]store.MessageAttachment, 0, len(attached))
	for _, att := range attached {
		wireAtts = append(wireAtts, att.AsMessageAttachment())
	}
	a.live.publish(runEvent{
		ChatID: req.ChatID, MessageID: req.MessageID, Seq: 1, Type: "started",
		Prompt: req.Prompt, Attachments: wireAtts, Model: req.Model.ID, CreatedAt: time.Now().UnixMilli(),
	})
	go func() {
		defer cancel()
		terminal := a.streamRunForProvider(ctx, userID, ownerID, targetProvider, req, attached)
		release(&terminal)
	}()
	return nil
}

func (a *API) streamRun(ctx context.Context, userID, ownerID string, req runRequest) runEvent {
	return a.streamRunForProvider(ctx, userID, ownerID, strings.ToLower(strings.TrimSpace(req.Provider)), req, nil)
}

func (a *API) streamRunForProvider(ctx context.Context, userID, ownerID, targetProvider string, req runRequest, attached []store.Attachment) runEvent {
	seq := 1
	failure := func(code, message string, err error) runEvent {
		a.logger.Error("stream response", "chatId", req.ChatID, "code", code, "error", err)
		return req.failure(seq+1, code, message, true)
	}
	queueCtx, queueCancel := context.WithTimeout(ctx, 5*time.Minute)
	select {
	case a.live.slots <- struct{}{}:
		defer func() { <-a.live.slots }()
	case <-queueCtx.Done():
		queueCancel()
		return failure("run_interrupted", "The response was interrupted.", queueCtx.Err())
	}
	queueErr := queueCtx.Err()
	queueCancel()
	// Revoking a user cancels all of their runs under this lock. A queued
	// run must see that cancellation even if an active run just freed a slot.
	a.live.mu.Lock()
	err := ctx.Err()
	a.live.mu.Unlock()
	if err == nil {
		err = queueErr
	}
	if err != nil {
		return failure("run_interrupted", "The response was interrupted.", err)
	}
	runCtx, runCancel := context.WithTimeout(ctx, 5*time.Minute)
	defer runCancel()
	sessionID, chatProvider, sessionProvider, shared, err := a.db.ChatRunSessionState(runCtx, req.ChatID)
	if err != nil {
		return failure("storage_error", "Could not open the chat.", err)
	}
	if targetProvider == "" {
		targetProvider = chatProvider
	}
	// A queued collaborator can lose access before a slot opens. Never
	// apply their private profile to somebody else's chat.
	if userID != ownerID {
		if !shared {
			return req.failure(seq+1, "chat_not_found", "Chat not found.", false)
		}
		if err := a.db.CheckChatWrite(runCtx, userID, req.ChatID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return req.failure(seq+1, "chat_not_found", "Chat not found.", false)
			}
			return failure("storage_error", "Could not open the chat.", err)
		}
	}
	backend, ok := a.live.provider(targetProvider)
	if !ok {
		return req.failure(seq+1, "provider_unavailable", "The provider for this chat is not available.", false)
	}
	var promptImages []provider.ImageAttachment
	for _, att := range attached {
		if att.Kind == string(uploads.KindImage) {
			if targetProvider == "antigravity" {
				return req.failure(seq+1, "unsupported_attachment", provider.ErrUnsupportedImageAttachments.Error(), false)
			}
			imgBytes, err := a.db.ReadAttachmentBytes(att.StorageKey)
			if err != nil {
				return failure("storage_error", "Could not load attached image.", err)
			}
			promptImages = append(promptImages, provider.ImageAttachment{
				Name:     att.OriginalName,
				MIMEType: att.MIMEType,
				Data:     imgBytes,
			})
		}
	}

	resumeSessionID := ""
	prompt := buildPromptWithAttachmentsBudget(req.Prompt, attached, a.config.EffectiveMaxAttachmentContextBytes())
	if sessionID != "" && targetProvider == sessionProvider {
		resumeSessionID = sessionID
	} else {
		prior, err := a.db.ChatPriorMessages(runCtx, req.ChatID, req.MessageID)
		if err != nil {
			return failure("storage_error", "Could not load chat history.", err)
		}
		priorAtts, err := a.db.PriorAttachmentsForChat(runCtx, req.ChatID)
		if err != nil {
			return failure("storage_error", "Could not load prior attachments.", err)
		}
		prompt = buildHandoffPromptWithAttachments(prior, priorAtts, prompt)
	}
	instructions, err := a.userInstructions(runCtx, userID, shared)
	if err != nil {
		return failure("storage_error", "Could not load your profile.", err)
	}
	started := time.Now()
	events, err := backend.Stream(runCtx, provider.Request{UserID: ownerID, Model: req.Model.ID, Reasoning: req.Reasoning, Prompt: prompt, SessionID: resumeSessionID, SystemInstructions: instructions, Images: promptImages})
	if err != nil {
		if errors.Is(err, provider.ErrUnsupportedImageAttachments) {
			return req.failure(seq+1, "unsupported_attachment", err.Error(), false)
		}
		return failure("provider_error", "Could not start the provider.", err)
	}
	var text, thinking bytes.Buffer
	for {
		select {
		case <-runCtx.Done():
			return failure("run_interrupted", "The response was interrupted.", runCtx.Err())
		case event, ok := <-events:
			if err := runCtx.Err(); err != nil {
				return failure("run_interrupted", "The response was interrupted.", err)
			}
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
				if err := a.finishStream(runCtx, userID, targetProvider, req, text.String(), thinking.String(), event, durationMs, shared); err != nil {
					return failure("storage_error", "Could not save the response.", err)
				}
				wire.Reason = "completed"
				wire.DurationMs = &durationMs
				wire.Provider = targetProvider
				wire.Model = req.Model.ID
				if event.Usage != nil {
					wire.Usage = &store.MessageUsage{
						InputTokens:       event.Usage.InputTokens,
						CachedInputTokens: event.Usage.CachedInputTokens,
						OutputTokens:      event.Usage.OutputTokens,
					}
				}
			case provider.Error:
				return failure("provider_error", "The provider could not complete the response.", event.Err)
			default:
				continue
			}
			seq++
			if event.Type == provider.Done {
				return wire
			}
			a.live.publish(wire)
		}
	}
}

func (a *API) finishStream(ctx context.Context, userID, targetProvider string, req runRequest, text, thinking string, event provider.Event, durationMs int64, shared bool) error {
	// Access revocation can make a running shared chat private. Do not
	// restore a session that the sharing transition already invalidated.
	a.live.mu.Lock()
	defer a.live.mu.Unlock()
	currentlyShared, err := a.db.IsChatShared(ctx, req.ChatID)
	if err != nil {
		return err
	}
	if shared != currentlyShared {
		event.SessionID = ""
	}
	return a.db.FinishRunWithProvider(ctx, userID, req.ChatID, req.MessageID, targetProvider, req.Model.ID, text, thinking, event.SessionID, durationMs, event.Usage)
}

const handoffTranscriptBudget = 24 << 10

func tailValidUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	start := len(s) - maxBytes
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

func buildHandoffPrompt(messages []store.Message, currentPrompt string) string {
	return buildHandoffPromptWithAttachments(messages, nil, currentPrompt)
}

func buildHandoffPromptWithAttachments(messages []store.Message, priorAtts map[string][]store.Attachment, currentPrompt string) string {
	const truncationNotice = "[Более ранние сообщения чата были сокращены из-за ограничения размера.]\n\n"
	type entry struct {
		prefix  string
		content string
	}
	entries := make([]entry, 0, len(messages))
	for _, m := range messages {
		content := strings.TrimSpace(strings.ToValidUTF8(m.Content, ""))
		if atts := priorAtts[m.ID]; m.Role == "user" && len(atts) > 0 {
			content = buildPromptWithAttachments(content, atts)
		}
		if content == "" {
			continue
		}
		var label string
		switch m.Role {
		case "user":
			label = "Пользователь"
		case "assistant":
			label = "Ассистент"
		default:
			continue
		}
		entries = append(entries, entry{prefix: label + ": ", content: content})
	}
	if len(entries) == 0 {
		return currentPrompt
	}

	parts := make([]string, 0, len(entries))
	used := 0
	truncated := false
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		part := e.prefix + e.content + "\n\n"
		if used+len(part) <= handoffTranscriptBudget {
			parts = append(parts, part)
			used += len(part)
			continue
		}
		truncated = true
		if len(parts) == 0 {
			avail := handoffTranscriptBudget - len(truncationNotice) - len(e.prefix) - 2
			clipped := tailValidUTF8(e.content, avail)
			if clipped != "" {
				parts = append(parts, e.prefix+clipped+"\n\n")
				used += len(e.prefix) + len(clipped) + 2
			}
		}
		break
	}
	if truncated {
		for used+len(truncationNotice) > handoffTranscriptBudget && len(parts) > 1 {
			used -= len(parts[len(parts)-1])
			parts = parts[:len(parts)-1]
		}
		if used+len(truncationNotice) > handoffTranscriptBudget && len(parts) == 1 {
			last := parts[0]
			if idx := strings.Index(last, ": "); idx >= 0 {
				prefix := last[:idx+2]
				body := strings.TrimSuffix(last[idx+2:], "\n\n")
				avail := handoffTranscriptBudget - len(truncationNotice) - len(prefix) - 2
				parts[0] = prefix + tailValidUTF8(body, avail) + "\n\n"
			}
		}
	}

	var b strings.Builder
	b.WriteString("Ниже история предыдущей переписки в этом чате. Продолжи диалог с учётом этого контекста, не повторяй эту инструкцию и не комментируй переключение провайдера.\n\n")
	if truncated {
		b.WriteString(truncationNotice)
	}
	for i := len(parts) - 1; i >= 0; i-- {
		b.WriteString(parts[i])
	}
	b.WriteString("Новый запрос пользователя:\n")
	b.WriteString(currentPrompt)
	return b.String()
}

func (req runRequest) failure(seq int, code, message string, retryable bool) runEvent {
	return runEvent{ChatID: req.ChatID, MessageID: req.MessageID, Seq: seq, Type: "error", Code: code, Message: message, Retryable: &retryable}
}
