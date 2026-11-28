package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"agent-console/server/internal/config"
	"agent-console/server/internal/provider"
	"agent-console/server/internal/store"
	"github.com/gorilla/websocket"
)

func TestWebSocketChatLifecycle(t *testing.T) {
	t.Setenv("PRO_LIMIT_5H_CENTS", "1")
	t.Setenv("PRO_LIMIT_7D_CENTS", "2")
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	userToken := func(name string) (string, string) {
		t.Helper()
		if err := db.CreateUser(t.Context(), name, []byte("test")); err != nil {
			t.Fatal(err)
		}
		user, _, err := db.Credentials(t.Context(), name)
		if err != nil {
			t.Fatal(err)
		}
		_, token, err := db.IssueDevice(t.Context(), user.ID, "test")
		if err != nil {
			t.Fatal(err)
		}
		return user.ID, token
	}
	alice, token := userToken("alice")
	bobID, bobToken := userToken("bob")
	_, secondToken, err := db.IssueDevice(t.Context(), alice, "second device")
	if err != nil {
		t.Fatal(err)
	}
	chat, err := db.CreateChat(t.Context(), alice, "Title", "stub", "default")
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateChat(t.Context(), alice, "Independent", "stub", "default")
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{Provider: "stub", CORSOrigin: "http://localhost:1420, tauri://localhost", MaxConcurrentStreams: 1}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	gate := make(chan struct{})
	requests := make(chan provider.Request, 12)
	handler := newHandler(ctx, db, cfg, logger, gatedStub{first: gate, requests: requests})
	name, preference, memory := "Имя Алисы", "Предпочтения Алисы", "Личная память Алисы"
	if _, err := db.UpdateUserProfile(ctx, alice, &name, &preference, &memory, nil); err != nil {
		t.Fatal(err)
	}
	bobName, bobMemory := "Имя Боба", "Личная память Боба"
	if _, err := db.UpdateUserProfile(ctx, bobID, &bobName, nil, &bobMemory, nil); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?ticket="
	const origin = "tauri://localhost"
	issueTicket := func(token string) string {
		t.Helper()
		req, err := http.NewRequest("POST", server.URL+"/api/ws/ticket", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Origin", origin)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var result struct{ Ticket string }
		if res.StatusCode != 200 || res.Header.Get("Access-Control-Allow-Origin") != origin {
			t.Fatalf("ticket response: %d %v", res.StatusCode, res.Header)
		}
		if err := json.NewDecoder(res.Body).Decode(&result); err != nil || result.Ticket == "" {
			t.Fatalf("missing ticket: %v", err)
		}
		return result.Ticket
	}
	dial := func(ticket, origin string, status int) *websocket.Conn {
		t.Helper()
		headers := http.Header{"Origin": []string{origin}}
		c, res, err := websocket.DefaultDialer.Dial(wsURL+url.QueryEscape(ticket), headers)
		if c != nil {
			t.Cleanup(func() { c.Close() })
		}
		if res != nil && res.Body != nil {
			res.Body.Close()
		}
		if res == nil || res.StatusCode != status || (status == 101 && err != nil) {
			t.Fatalf("dial response %v, error %v", res, err)
		}
		return c
	}
	dial("", origin, 401)
	ticket := issueTicket(token)
	dial(ticket, "https://other.example", 403)
	writer := dial(ticket, origin, 101)
	dial(ticket, origin, 401)
	watcher := dial(issueTicket(secondToken), origin, 101)
	bob := dial(issueTicket(bobToken), origin, 101)
	write := func(c *websocket.Conn, chatID, messageID, mode string) {
		t.Helper()
		if err := c.WriteJSON(map[string]any{
			"chatId": chatID, "messageId": messageID, "prompt": "Привет",
			"model":  map[string]string{"id": "codex", "provider": "codex"},
			"target": map[string]string{"mode": mode}, "history": []any{},
		}); err != nil {
			t.Fatal(err)
		}
	}
	read := func(c *websocket.Conn) runEvent {
		t.Helper()
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		var event runEvent
		if err := c.ReadJSON(&event); err != nil {
			t.Fatal(err)
		}
		return event
	}
	expectError := func(event runEvent, id, code string, retryable bool) {
		t.Helper()
		if event.Type != "error" || event.ChatID != chat.ID || event.MessageID != id || event.Code != code ||
			event.Seq != 1 || event.Message == "" || event.Retryable == nil || *event.Retryable != retryable || event.Delta != "" || event.Reason != "" {
			t.Fatalf("incorrect error envelope: %#v", event)
		}
	}
	write(writer, chat.ID, "mcp", "mcp")
	expectError(read(writer), "mcp", "invalid_request", false)
	write(writer, chat.ID, "answer-1", "chat")
	started := read(writer)
	if started.Type != "started" || started.Seq != 1 || started.Prompt != "Привет" || !reflect.DeepEqual(started, read(watcher)) {
		t.Fatalf("missing start frame: %#v", started)
	}
	nextRequest := func() provider.Request {
		t.Helper()
		select {
		case request := <-requests:
			return request
		case <-time.After(3 * time.Second):
			t.Fatal("provider request not received")
			return provider.Request{}
		}
	}
	personal := nextRequest()
	for _, expected := range []string{name, preference, memory} {
		if !strings.Contains(personal.SystemInstructions, expected) {
			t.Fatalf("omitted %q: %q", expected, personal.SystemInstructions)
		}
	}
	first := read(writer)
	if first.Type != "thinking_delta" || first.Seq != 2 || first.Delta == "" {
		t.Fatalf("first event: %#v", first)
	}
	if got := read(watcher); !reflect.DeepEqual(first, got) {
		t.Fatalf("different first frame on second device: %#v", got)
	}
	write(bob, chat.ID, "foreign", "chat")
	expectError(read(bob), "foreign", "chat_not_found", false)
	write(watcher, chat.ID, "too-soon", "chat")
	expectError(read(watcher), "too-soon", "chat_busy", true)

	// The prompt is present even while the assistant is still streaming.
	messages, err := db.Messages(t.Context(), alice, chat.ID)
	if err != nil || len(messages) != 1 || messages[0].Role != "user" {
		t.Fatalf("missing in-flight prompt: %v %v", messages, err)
	}
	close(gate)
	for seq := 3; seq <= 6; seq++ {
		got := read(writer)
		if got.MessageID != "answer-1" || got.ChatID != chat.ID || got.Seq != seq || !reflect.DeepEqual(got, read(watcher)) {
			t.Fatalf("stream mismatch: %#v", got)
		}
		if seq < 6 && (got.Type != "text_delta" || got.Delta == "" || got.Retryable != nil) {
			t.Fatalf("invalid text delta: %#v", got)
		}
		if seq == 6 && (got.Type != "done" || got.Reason != "completed" || got.Delta != "") {
			t.Fatalf("invalid done: %#v", got)
		}
	}

	_ = writer.Close()
	writer = dial(issueTicket(token), origin, 101)
	write(writer, chat.ID, "answer-1", "chat")
	expectError(read(writer), "answer-1", "duplicate_run", false)
	write(writer, other.ID, "answer-1", "chat")
	_ = writer.Close() // The run continues when the sending device disconnects.
	nextRequest()
	for seq := 1; seq <= 6; seq++ {
		got := read(watcher)
		if got.ChatID != other.ID || got.MessageID != "answer-1" || got.Seq != seq {
			t.Fatalf("unexpected independent run frame: %#v", got)
		}
	}

	reopened, err := store.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	req, err := http.NewRequest("GET", server.URL+"/api/chats/"+chat.ID+"/messages", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var history []store.Message
	if err := json.NewDecoder(res.Body).Decode(&history); err != nil {
		t.Fatal(err)
	}
	persisted, err := reopened.Messages(t.Context(), alice, chat.ID)
	if err != nil || len(history) != 2 || !reflect.DeepEqual(history, persisted) ||
		history[0].Content != "Привет" || history[1].ID != "answer-1" || history[1].Role != "assistant" ||
		history[1].Content != "Получен запрос: Привет\n\nОтвет от StubProvider." || history[1].Thinking != first.Delta {
		t.Fatalf("incorrect durable history, HTTP: %v SQLite: %v (%v)", history, persisted, err)
	}
	api := func(method, path, auth, body string, status int, result any) {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+auth)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != status {
			t.Fatalf("%s %s status: %d, want %d", method, path, res.StatusCode, status)
		}
		if result != nil {
			if err := json.NewDecoder(res.Body).Decode(result); err != nil {
				t.Fatal(err)
			}
		}
	}
	var directory []store.DirectoryUser
	api("GET", "/api/users", bobToken, "", 200, &directory)
	if len(directory) != 2 {
		t.Fatalf("directory: %+v", directory)
	}
	chatPath := "/api/chats/" + chat.ID
	sharePath := chatPath + "/shares/" + bobID
	share := func(method, body string) {
		t.Helper()
		api(method, sharePath, token, body, 204, nil)
		for _, c := range []*websocket.Conn{watcher, bob} {
			if event := read(c); event.Type != "workspace_changed" || event.ChatID != chat.ID {
				t.Fatalf("missing workspace change: %+v", event)
			}
		}
	}
	sharedRun := func(sender, observer *websocket.Conn, id, speaker string) {
		t.Helper()
		write(sender, chat.ID, id, "chat")
		request := nextRequest()
		if request.SystemInstructions != "Текущий собеседник: "+speaker {
			t.Fatalf("shared context: %q", request.SystemInstructions)
		}
		if id == "shared-read" && request.SessionID != "" {
			t.Fatal("shared chat resumed a private session")
		}
		if id == "shared-write" && request.SessionID != "stub-session" {
			t.Fatal("shared chat did not resume its own session")
		}
		for seq := 1; seq <= 6; seq++ {
			event := read(sender)
			if event.MessageID != id || event.Seq != seq || !reflect.DeepEqual(event, read(observer)) ||
				(seq == 1 && event.Type != "started") || (seq == 6 && event.Type != "done") {
				t.Fatalf("shared stream mismatch: %+v", event)
			}
		}
	}
	share("PUT", `{"access":"read"}`)
	write(bob, chat.ID, "read-only", "chat")
	expectError(read(bob), "read-only", "chat_not_found", false)
	sharedRun(watcher, bob, "shared-read", name)
	share("PUT", `{"access":"write"}`)
	tier := "pro"
	if err := db.UpdateAdminUser(t.Context(), bobID, nil, nil, nil, store.SubscriptionUpdate{PlanTier: &tier}); err != nil {
		t.Fatal(err)
	}
	sharedRun(bob, watcher, "shared-write", bobName)
	var quota store.Quota
	api("GET", "/api/quotas/current", bobToken, "", 200, &quota)
	if quota.FiveHour.UsedCents != 1 || quota.FiveHour.UsedPercent != 100 || quota.SevenDay.UsedPercent != 50 {
		t.Fatalf("writer quota after successful stub run: %+v", quota)
	}
	if used, _, err := db.UserUsage(t.Context(), alice); err != nil || used != 3 {
		t.Fatalf("owner quota: %d (%v)", used, err)
	}
	write(bob, chat.ID, "five-hour", "chat")
	expectError(read(bob), "five-hour", "five_hour_limit_reached", false)
	bypass := true
	if err := db.UpdateAdminUser(t.Context(), bobID, nil, nil, nil, store.SubscriptionUpdate{Bypass5hLimit: &bypass}); err != nil {
		t.Fatal(err)
	}
	sharedRun(bob, watcher, "shared-bypass", bobName)
	write(bob, chat.ID, "weekly", "chat")
	expectError(read(bob), "weekly", "weekly_limit_reached", false)
	share("PUT", `{"access":"fork"}`)
	var fork store.Chat
	api("POST", chatPath+"/fork", bobToken, "", 201, &fork)
	if fork.ID == chat.ID || fork.OwnerID != bobID || fork.Access != "owner" {
		t.Fatalf("fork: %+v", fork)
	}
	share("DELETE", "")
	api("GET", chatPath+"/messages", bobToken, "", 404, nil)
	api("GET", "/api/chats/"+fork.ID+"/messages", bobToken, "", 200, &history)
	if len(history) != 8 {
		t.Fatalf("copied messages: %d", len(history))
	}
	write(watcher, chat.ID, "after-revoke", "chat")
	for range 6 {
		read(watcher)
	}
	_ = bob.SetReadDeadline(time.Now().Add(80 * time.Millisecond))
	if _, _, err := bob.ReadMessage(); err == nil {
		t.Fatal("revoked socket received a broadcast")
	}

}

type gatedStub struct {
	first    <-chan struct{}
	requests chan<- provider.Request
}

func (s gatedStub) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	if s.requests != nil {
		s.requests <- req
	}
	source, err := (provider.StubProvider{Delay: time.Millisecond}).Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	out := make(chan provider.Event)
	go func() {
		defer close(out)
		for event := range source {
			if event.Type == provider.Done {
				event.SessionID = "stub-session"
			}
			select {
			case out <- event:
			case <-ctx.Done():
				return
			}
			if event.Type == provider.Thinking {
				select {
				case <-s.first:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

func TestDisconnectUserCancelsActiveAndQueuedRuns(t *testing.T) {
	ctx := t.Context()
	started := make(chan context.Context, 2)
	db, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUserWithRole(ctx, "alice", []byte("test"), "user")
	if err != nil {
		t.Fatal(err)
	}
	a := &API{
		db:     db,
		live:   newLiveRuns(ctx, blockingProvider{started}, 1),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	terminal := make(chan runEvent, 2)
	start := func(userID, chatID string) {
		runCtx, cancel := context.WithCancel(ctx)
		t.Cleanup(cancel)
		release, code := a.live.acquire(userID, chatID, &socket{deviceID: "device"}, cancel)
		if release == nil {
			t.Fatal(code)
		}
		go func() {
			event := a.streamRun(runCtx, userID, userID, runRequest{ChatID: chatID})
			release(nil)
			terminal <- event
		}()
	}
	active, err := db.CreateChat(ctx, user.ID, "active", "stub", "default")
	if err != nil {
		t.Fatal(err)
	}
	queued, err := db.CreateChat(ctx, user.ID, "queued", "stub", "default")
	if err != nil {
		t.Fatal(err)
	}
	start(user.ID, active.ID)
	var activeCtx context.Context
	select {
	case activeCtx = <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("provider did not start")
	}
	start(user.ID, queued.ID)
	a.live.disconnectUser("bob")
	if activeCtx.Err() != nil {
		t.Fatal("disconnecting another user canceled the run")
	}
	a.live.disconnectUser(user.ID)
	for range 2 {
		select {
		case event := <-terminal:
			if event.Code != "run_interrupted" {
				t.Fatalf("unexpected terminal event: %+v", event)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("run did not cancel")
		}
	}
	select {
	case <-started:
		t.Fatal("queued run started")
	default:
	}
	if len(a.live.slots) != 0 {
		t.Fatal("stream slot leaked")
	}
}

type blockingProvider struct {
	started chan<- context.Context
}

func (p blockingProvider) Stream(ctx context.Context, _ provider.Request) (<-chan provider.Event, error) {
	p.started <- ctx
	return make(chan provider.Event), nil
}

type memoryStub struct {
	provider.StubProvider
	requests chan<- provider.Request
}

func (s memoryStub) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	s.requests <- req
	status, err := s.StubProvider.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	out := make(chan provider.Event, 2)
	go func() {
		defer close(out)
		for event := range status {
			if event.Type == provider.Done {
				out <- provider.Event{Type: provider.Text, Delta: "- Пользователь использует Go."}
				out <- event
			}
		}
	}()
	return out, nil
}

func TestBackgroundMemoryUsesPrivateChatsAndNoQuota(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	db, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	defer cancel()
	owner, err := db.CreateUserWithRole(ctx, "owner", []byte("hash"), "admin")
	if err != nil {
		t.Fatal(err)
	}
	guest, err := db.CreateUserWithRole(ctx, "guest", []byte("hash"), "user")
	if err != nil {
		t.Fatal(err)
	}
	chatWithMessages := func(shared bool, prompt string) {
		t.Helper()
		chat, err := db.CreateChat(ctx, owner.ID, "title", "stub", "default")
		if err != nil {
			t.Fatal(err)
		}
		if shared {
			if err = db.SetChatShare(ctx, owner.ID, chat.ID, guest.ID, "read"); err != nil {
				t.Fatal(err)
			}
		}
		if _, err = db.BeginRun(ctx, owner.ID, chat.ID, "seed", prompt, "stub", "default"); err != nil {
			t.Fatal(err)
		}
		if err = db.FinishRun(ctx, owner.ID, chat.ID, "seed", "Понятно.", "", "session", 0, nil); err != nil {
			t.Fatal(err)
		}
	}
	chatWithMessages(false, "Я использую Go.")
	chatWithMessages(true, "SHARED-ONLY")
	before, _, err := db.UserUsage(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan provider.Request, 2)
	cfg := config.Config{Provider: "stub", MemoryAutoUpdate: true, MemoryCheckInterval: 10 * time.Millisecond, MemoryMinNewMessages: 2, MemoryModel: "light"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	newHandler(ctx, db, cfg, logger, memoryStub{requests: requests})
	select {
	case req := <-requests:
		if req.UserID != owner.ID || req.Model != "light" || req.SystemInstructions != "" || req.SessionID != "" || !req.Ephemeral ||
			!strings.Contains(req.Prompt, "Я использую Go.") || strings.Contains(req.Prompt, "SHARED-ONLY") {
			t.Fatalf("invalid memory request: %+v", req)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("memory worker did not start")
	}
	for deadline := time.Now().Add(3 * time.Second); ; {
		got, err := db.UserProfile(ctx, owner.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.MemoryUpdatedAt > 0 {
			if got.MemoryMD != "- Пользователь использует Go." {
				t.Fatalf("memory result: %+v", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("memory not saved")
		}
		time.Sleep(5 * time.Millisecond)
	}
	after, _, err := db.UserUsage(ctx, owner.ID)
	if err != nil || after != before {
		t.Fatalf("background charge: %d -> %d (%v)", before, after, err)
	}
}

func TestRevokedWriterCannotProvidePrivateContextToOwner(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	owner, err := db.CreateUserWithRole(ctx, "owner", []byte("hash"), "user")
	if err != nil {
		t.Fatal(err)
	}
	guest, err := db.CreateUserWithRole(ctx, "guest", []byte("hash"), "user")
	if err != nil {
		t.Fatal(err)
	}
	guestMemory := "GUEST PRIVATE MEMORY"
	if _, err := db.UpdateUserProfile(ctx, guest.ID, nil, nil, &guestMemory, nil); err != nil {
		t.Fatal(err)
	}
	chat, err := db.CreateChat(ctx, owner.ID, "test", "stub", "default")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.SetChatShare(ctx, owner.ID, chat.ID, guest.ID, "write"); err != nil {
		t.Fatal(err)
	}
	req := runRequest{ChatID: chat.ID, MessageID: "queued", Prompt: "Hi"}
	if _, err = db.BeginRun(ctx, guest.ID, chat.ID, req.MessageID, req.Prompt, "stub", "default"); err != nil {
		t.Fatal(err)
	}
	// The owner revokes access before the provider slot becomes available.
	if err = db.SetChatShare(ctx, owner.ID, chat.ID, guest.ID, ""); err != nil {
		t.Fatal(err)
	}
	captured := make(chan provider.Request, 1)
	gate := make(chan struct{})
	close(gate)
	backend := gatedStub{first: gate, requests: captured}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := &API{db: db, logger: logger, live: newLiveRuns(ctx, backend, 1)}
	a.live.db = db
	a.live.logger = logger
	if event := a.streamRun(ctx, guest.ID, owner.ID, req); event.Code != "chat_not_found" {
		t.Fatalf("revoked run was accepted: %+v", event)
	}
	select {
	case run := <-captured:
		t.Fatalf("provider received a revoked request: %+v", run)
	default:
	}
	if session, _, err := db.ChatRunContext(ctx, chat.ID); session != "" || err != nil {
		t.Fatalf("saved session from revoked writer: %q %v", session, err)
	}
}

type switchCaptureProvider struct {
	name     string
	seq      *atomic.Int64
	requests chan<- capturedProviderCall
}

type capturedProviderCall struct {
	provider string
	req      provider.Request
}

func (p switchCaptureProvider) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	n := p.seq.Add(1)
	sessionID := p.name + "-sess-" + string(rune('0'+n))
	reply := "Ответ от " + p.name + " #" + string(rune('0'+n))
	p.requests <- capturedProviderCall{provider: p.name, req: req}
	out := make(chan provider.Event, 2)
	go func() {
		defer close(out)
		out <- provider.Event{Type: provider.Text, Delta: reply}
		out <- provider.Event{Type: provider.Done, SessionID: sessionID}
	}()
	return out, nil
}

func TestWebSocketInChatProviderSwitchAndHandoff(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	db, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	createUser := func(name string) (string, string) {
		t.Helper()
		u, err := db.CreateUserWithRole(ctx, name, []byte("hash"), "admin")
		if err != nil {
			t.Fatal(err)
		}
		_, tok, err := db.IssueDevice(ctx, u.ID, "browser")
		if err != nil {
			t.Fatal(err)
		}
		return u.ID, tok
	}
	aliceID, aliceToken := createUser("alice")
	bobID, bobToken := createUser("bob")

	aliceProfile := "PRIVATE PROFILE"
	aliceMemory := "PRIVATE MEMORY"
	if _, err := db.UpdateUserProfile(ctx, aliceID, nil, &aliceProfile, &aliceMemory, nil); err != nil {
		t.Fatal(err)
	}
	bobProfile := "BOB PRIVATE PROFILE"
	bobMemory := "BOB PRIVATE MEMORY"
	if _, err := db.UpdateUserProfile(ctx, bobID, nil, &bobProfile, &bobMemory, nil); err != nil {
		t.Fatal(err)
	}

	chat, err := db.CreateChat(ctx, aliceID, "Multi", "codex", "gpt-5")
	if err != nil {
		t.Fatal(err)
	}

	captured := make(chan capturedProviderCall, 10)
	var codexSeq, claudeSeq atomic.Int64
	cfg := config.Config{
		Provider:             "codex",
		EnabledProviders:     []string{"codex", "claude"},
		DefaultProvider:      "codex",
		MemoryProvider:       "codex",
		CORSOrigin:           "*",
		MaxConcurrentStreams: 2,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := newRegistryHandler(ctx, db, cfg, logger, map[string]provider.Provider{
		"codex":  switchCaptureProvider{name: "codex", seq: &codexSeq, requests: captured},
		"claude": switchCaptureProvider{name: "claude", seq: &claudeSeq, requests: captured},
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	dialWS := func(token string) *websocket.Conn {
		t.Helper()
		req, _ := http.NewRequest("POST", srv.URL+"/api/ws/ticket", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		if err != nil || res.StatusCode != 200 {
			t.Fatalf("ticket err=%v res=%v", err, res)
		}
		var body struct{ Ticket string }
		_ = json.NewDecoder(res.Body).Decode(&body)
		res.Body.Close()
		wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws?ticket=" + url.QueryEscape(body.Ticket)
		conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		return conn
	}
	aliceWS := dialWS(aliceToken)
	bobWS := dialWS(bobToken)

	sendAndWaitDone := func(ws *websocket.Conn, msgID, prov, modelID, prompt string) {
		t.Helper()
		payload := map[string]any{
			"chatId":    chat.ID,
			"messageId": msgID,
			"prompt":    prompt,
			"model":     map[string]string{"provider": prov, "id": modelID},
			"target":    map[string]string{"mode": "chat"},
		}
		if prov != "" {
			payload["provider"] = prov
		}
		if err := ws.WriteJSON(payload); err != nil {
			t.Fatal(err)
		}
		for {
			_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
			var ev runEvent
			if err := ws.ReadJSON(&ev); err != nil {
				t.Fatal(err)
			}
			if ev.MessageID != msgID {
				continue
			}
			if ev.Type == "error" {
				t.Fatalf("unexpected error for %s: %+v", msgID, ev)
			}
			if ev.Type == "done" {
				return
			}
		}
	}
	nextCall := func() capturedProviderCall {
		t.Helper()
		select {
		case c := <-captured:
			return c
		case <-time.After(3 * time.Second):
			t.Fatal("missing provider call")
			return capturedProviderCall{}
		}
	}

	// 1. First turn on provider A (codex).
	sendAndWaitDone(aliceWS, "m1", "codex", "gpt-5", "Запомни секретное число 42.")
	c1 := nextCall()
	if c1.provider != "codex" || c1.req.SessionID != "" || c1.req.Prompt != "Запомни секретное число 42." {
		t.Fatalf("turn 1 call: %+v", c1)
	}
	sess1, prov1, sessProv1, _, err := db.ChatRunSessionState(ctx, chat.ID)
	if err != nil || prov1 != "codex" || sessProv1 != "codex" || sess1 != "codex-sess-1" {
		t.Fatalf("post turn 1 state: (%q, %q, %q, %v)", sess1, prov1, sessProv1, err)
	}

	// 2. Switch to provider B (claude) in the same chat: no codex session ID, SQLite history handoff included.
	sendAndWaitDone(aliceWS, "m2", "claude", "sonnet", "Какое число я назвал?")
	c2 := nextCall()
	if c2.provider != "claude" || c2.req.SessionID != "" {
		t.Fatalf("turn 2 must not reuse codex session: %+v", c2)
	}
	if !strings.Contains(c2.req.Prompt, "Пользователь: Запомни секретное число 42.") ||
		!strings.Contains(c2.req.Prompt, "Ассистент: Ответ от codex #1") ||
		!strings.Contains(c2.req.Prompt, "Какое число я назвал?") ||
		strings.Count(c2.req.Prompt, "Какое число я назвал?") != 1 {
		t.Fatalf("turn 2 handoff prompt invalid: %q", c2.req.Prompt)
	}
	sess2, prov2, sessProv2, _, err := db.ChatRunSessionState(ctx, chat.ID)
	if err != nil || prov2 != "claude" || sessProv2 != "claude" || sess2 != "claude-sess-1" {
		t.Fatalf("post turn 2 state: (%q, %q, %q, %v)", sess2, prov2, sessProv2, err)
	}

	// 3. Follow-up turn on B (claude): resumes claude-sess-1 WITHOUT repeating handoff.
	sendAndWaitDone(aliceWS, "m3", "claude", "opus", "Уточни подробнее")
	c3 := nextCall()
	if c3.provider != "claude" || c3.req.SessionID != "claude-sess-1" || c3.req.Prompt != "Уточни подробнее" {
		t.Fatalf("turn 3 should resume B cleanly without handoff: %+v", c3)
	}
	if sess3, _, sessProv3, _, err := db.ChatRunSessionState(ctx, chat.ID); err != nil || sess3 != "claude-sess-2" || sessProv3 != "claude" {
		t.Fatalf("post turn 3 state: (%q, %q, %v)", sess3, sessProv3, err)
	}

	// 4. Switch back from B to A (codex): fresh codex session with updated history including claude replies.
	sendAndWaitDone(aliceWS, "m4", "codex", "gpt-5", "Подведи итог")
	c4 := nextCall()
	if c4.provider != "codex" || c4.req.SessionID != "" {
		t.Fatalf("turn 4 back to A must start fresh session: %+v", c4)
	}
	if !strings.Contains(c4.req.Prompt, "Пользователь: Какое число я назвал?") ||
		!strings.Contains(c4.req.Prompt, "Ассистент: Ответ от claude #1") ||
		!strings.Contains(c4.req.Prompt, "Ассистент: Ответ от claude #2") ||
		!strings.Contains(c4.req.Prompt, "Подведи итог") {
		t.Fatalf("turn 4 handoff missing B replies: %q", c4.req.Prompt)
	}
	if sess4, prov4, sessProv4, _, err := db.ChatRunSessionState(ctx, chat.ID); err != nil || prov4 != "codex" || sessProv4 != "codex" || sess4 != "codex-sess-2" {
		t.Fatalf("post turn 4 state: (%q, %q, %q, %v)", sess4, prov4, sessProv4, err)
	}

	// 5. Attempt to switch existing chat to paid_api (or disabled antigravity) fails cleanly without mutating state.
	for _, bad := range []string{"paid_api", "antigravity"} {
		badID := "bad-" + bad
		if err := aliceWS.WriteJSON(map[string]any{
			"chatId": chat.ID, "messageId": badID, "provider": bad, "prompt": "Test",
			"model": map[string]string{"provider": bad, "id": "default"}, "target": map[string]string{"mode": "chat"},
		}); err != nil {
			t.Fatal(err)
		}
		_ = aliceWS.SetReadDeadline(time.Now().Add(3 * time.Second))
		var ev runEvent
		if err := aliceWS.ReadJSON(&ev); err != nil {
			t.Fatal(err)
		}
		if ev.Type != "error" || ev.Code != "provider_unavailable" {
			t.Fatalf("expected provider_unavailable for %s, got %+v", bad, ev)
		}
	}
	if sess, prov, sessProv, _, err := db.ChatRunSessionState(ctx, chat.ID); err != nil || prov != "codex" || sessProv != "codex" || sess != "codex-sess-2" {
		t.Fatalf("failed switch mutated state: (%q, %q, %q, %v)", sess, prov, sessProv, err)
	}

	// 6. Shared chat handoff preserves isolation (never includes private profile_md / memory_md).
	if err := db.SetChatShare(ctx, aliceID, chat.ID, bobID, "write"); err != nil {
		t.Fatal(err)
	}
	sendAndWaitDone(bobWS, "m5", "claude", "sonnet", "Вопрос участника")
	c5 := nextCall()
	if c5.provider != "claude" || c5.req.SessionID != "" {
		t.Fatalf("shared handoff call: %+v", c5)
	}
	if strings.Contains(c5.req.SystemInstructions, bobProfile) || strings.Contains(c5.req.SystemInstructions, bobMemory) ||
		strings.Contains(c5.req.SystemInstructions, aliceProfile) || strings.Contains(c5.req.SystemInstructions, aliceMemory) {
		t.Fatalf("shared chat leaked private profile/memory: %q", c5.req.SystemInstructions)
	}
	if !strings.Contains(c5.req.Prompt, "Ассистент: Ответ от codex #2") {
		t.Fatalf("shared handoff missing history: %q", c5.req.Prompt)
	}
}

func TestBuildHandoffPromptTruncationValidUTF8(t *testing.T) {
	oldMsg := store.Message{Role: "user", Content: "СТАРОЕ_СООБЩЕНИЕ_" + strings.Repeat("ж", 1000)}
	midMsg := store.Message{Role: "assistant", Content: strings.Repeat("Привет мир 🚀 ", 1200)}
	recentUser := store.Message{Role: "user", Content: "Свежий вопрос про тест"}
	recentBot := store.Message{Role: "assistant", Content: "Свежий ответ с деталями"}

	out := buildHandoffPrompt([]store.Message{oldMsg, midMsg, recentUser, recentBot}, "Текущий промпт")
	if !utf8.ValidString(out) {
		t.Fatal("handoff output is not valid UTF-8")
	}
	if !strings.Contains(out, "[Более ранние сообщения чата были сокращены из-за ограничения размера.]") {
		t.Fatalf("missing truncation notice in: %q", out[:min(len(out), 300)])
	}
	if strings.Contains(out, "СТАРОЕ_СООБЩЕНИЕ_") {
		t.Fatal("expected oldest message to be dropped")
	}
	if !strings.Contains(out, "Свежий вопрос про тест") || !strings.Contains(out, "Свежий ответ с деталями") || !strings.Contains(out, "Текущий промпт") {
		t.Fatal("missing recent messages or current prompt")
	}
	if len(out) > handoffTranscriptBudget+1024 {
		t.Fatalf("handoff exceeded budget: %d", len(out))
	}
}

type mockModelListerProvider struct {
	models []provider.Model
}

func (m mockModelListerProvider) Stream(context.Context, provider.Request) (<-chan provider.Event, error) {
	ch := make(chan provider.Event)
	close(ch)
	return ch, nil
}

func (m mockModelListerProvider) ListModels(context.Context) ([]provider.Model, error) {
	return m.models, nil
}

func TestResolveDefaultModelID(t *testing.T) {
	mockCodex := mockModelListerProvider{
		models: []provider.Model{
			{ID: "gpt-6.2-astra", Name: "GPT-6.2-Astra", IsDefault: false},
			{ID: "gpt-6.2-sol", Name: "GPT-6.2-Sol", IsDefault: true},
		},
	}
	api := &API{
		live: newLiveRuns(t.Context(), mockCodex, 1),
	}
	api.live.providers = map[string]provider.Provider{
		"codex": mockCodex,
	}
	if got := api.resolveDefaultModelID(t.Context(), "codex"); got != "gpt-6.2-sol" {
		t.Fatalf("expected gpt-6.2-sol, got %q", got)
	}
	if got := api.resolveDefaultModelID(t.Context(), "claude"); got != "sonnet" {
		t.Fatalf("expected fallback sonnet, got %q", got)
	}
	if !isDefaultModelPlaceholder("", "codex") || !isDefaultModelPlaceholder("default", "codex") || !isDefaultModelPlaceholder("codex", "codex") {
		t.Fatal("expected empty, default, and provider name to be treated as default placeholder")
	}
	if isDefaultModelPlaceholder("gpt-6.2-sol", "codex") {
		t.Fatal("explicit model slug must not be treated as placeholder")
	}
}
