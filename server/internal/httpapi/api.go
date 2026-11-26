package httpapi

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"agent-console/server/internal/config"
	"agent-console/server/internal/identity"
	"agent-console/server/internal/provider"
	"agent-console/server/internal/store"
	"golang.org/x/crypto/bcrypt"
)

type API struct {
	db      *store.Store
	config  config.Config
	logger  *slog.Logger
	live    *liveRuns
	catalog modelCatalog
	index   indexedHistory
	logins  loginLimiter
}

type session struct {
	User   store.User   `json:"user"`
	Device store.Device `json:"device"`
}

func New(ctx context.Context, db *store.Store, cfg config.Config, logger *slog.Logger) (http.Handler, error) {
	switch cfg.Provider {
	case "stub", "codex", "antigravity":
	case "claude", "paid_api":
		return nil, fmt.Errorf("provider %q is not implemented yet", cfg.Provider)
	default:
		return nil, fmt.Errorf("unsupported provider %q", cfg.Provider)
	}
	var backend provider.Provider
	if cfg.RunnerURL != "" {
		remote, err := provider.NewRemoteRunnerProvider(cfg.RunnerURL, cfg.RunnerSharedSecret)
		if err != nil {
			return nil, fmt.Errorf("configure runner: %w", err)
		}
		backend = remote
	} else if cfg.Provider == "stub" {
		backend = provider.StubProvider{Delay: 100 * time.Millisecond}
	} else {
		return nil, errors.New("RUNNER_URL is required for CLI providers")
	}
	return newHandler(ctx, db, cfg, logger, backend), nil
}

func newHandler(ctx context.Context, db *store.Store, cfg config.Config, logger *slog.Logger, backend provider.Provider) http.Handler {
	api := &API{db: db, config: cfg, logger: logger, live: newLiveRuns(ctx, backend, max(1, cfg.MaxConcurrentStreams))}
	api.live.db = db
	api.live.logger = logger
	if db != nil && cfg.MemoryAutoUpdate && cfg.MemoryCheckInterval > 0 {
		go api.runMemoryWorker(ctx)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", api.health)
	mux.HandleFunc("POST /api/auth/login", api.login)
	mux.HandleFunc("POST /api/auth/register", api.register)
	mux.HandleFunc("GET /api/auth/me", api.withPendingAuth(api.me))
	mux.HandleFunc("POST /api/auth/logout", api.withPendingAuth(api.logout))
	mux.HandleFunc("GET /api/devices", api.withPendingAuth(api.devices))
	mux.HandleFunc("DELETE /api/devices/{id}", api.withPendingAuth(api.deleteDevice))
	mux.HandleFunc("POST /api/ws/ticket", api.withAuth(api.issueWSTicket))
	mux.HandleFunc("GET /api/admin/users/{id}/chats", api.RequireAdmin(api.adminUserChats))
	mux.HandleFunc("GET /api/admin/users/{id}/chats/{chatId}/messages", api.RequireAdmin(api.adminChatMessages))
	mux.HandleFunc("GET /api/admin/users", api.RequireAdmin(api.adminUsers))
	mux.HandleFunc("POST /api/admin/users", api.RequireAdmin(api.adminCreateUser))
	mux.HandleFunc("PATCH /api/admin/users/{id}", api.RequireAdmin(api.adminUpdateUser))
	mux.HandleFunc("DELETE /api/admin/users/{id}/devices", api.RequireAdmin(api.adminRevokeDevices))
	mux.HandleFunc("GET /api/codex/chats", api.withAuth(api.indexedChats))
	mux.HandleFunc("GET /api/codex/chats/{id}/messages", api.withAuth(api.indexedMessages))
	mux.HandleFunc("GET /api/quotas/current", api.withAuth(api.currentQuota))
	mux.HandleFunc("GET /api/profile", api.withAuth(api.getProfile))
	mux.HandleFunc("PATCH /api/profile", api.withAuth(api.patchProfile))
	mux.HandleFunc("GET /api/models", api.withAuth(api.models))
	mux.HandleFunc("GET /api/users", api.withAuth(api.directoryUsers))
	mux.HandleFunc("GET /api/chats", api.withAuth(api.chats))
	mux.HandleFunc("POST /api/chats", api.withAuth(api.createChat))
	mux.HandleFunc("GET /api/chats/{id}/messages", api.withAuth(api.messages))
	mux.HandleFunc("PUT /api/chats/{id}/shares/{userId}", api.withAuth(api.putChatShare))
	mux.HandleFunc("DELETE /api/chats/{id}/shares/{userId}", api.withAuth(api.deleteChatShare))
	mux.HandleFunc("POST /api/chats/{id}/fork", api.withAuth(api.forkChat))
	mux.HandleFunc("GET /ws", api.websocket)
	return api.cors(mux)
}

func (a *API) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Add("Vary", "Origin")
		if origin := r.Header.Get("Origin"); origin != "" {
			if !a.allowsOrigin(origin) {
				fail(w, http.StatusForbidden, "origin_denied", "Origin is not allowed.")
				return
			}
			allowed := origin
			if a.config.CORSOrigin == "*" {
				allowed = "*"
			}
			w.Header().Set("Access-Control-Allow-Origin", allowed)
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) allowsOrigin(origin string) bool {
	if origin == "" || a.config.CORSOrigin == "*" {
		return true
	}
	for _, allowed := range strings.Split(a.config.CORSOrigin, ",") {
		if strings.TrimSpace(allowed) == origin {
			return true
		}
	}
	return false
}

type loginLimiter struct {
	mu          sync.Mutex
	attempts    map[string][]time.Time
	nextCleanup time.Time
}

func (l *loginLimiter) allow(key string, limit int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-time.Minute)
	if l.attempts == nil {
		l.attempts = make(map[string][]time.Time)
	}
	if !now.Before(l.nextCleanup) {
		for key, times := range l.attempts {
			if !times[len(times)-1].After(cutoff) {
				delete(l.attempts, key)
			}
		}
		l.nextCleanup = now.Add(time.Minute)
	}
	times := l.attempts[key]
	for len(times) > 0 && !times[0].After(cutoff) {
		times = times[1:]
	}
	if len(times) >= limit {
		return false
	}
	l.attempts[key] = append(times, now)
	return true
}

func (a *API) health(w http.ResponseWriter, _ *http.Request) {
	reply(w, http.StatusOK, map[string]string{"status": "ok", "provider": a.config.Provider})
}

var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("dummy login hash"), bcrypt.DefaultCost)

func (a *API) clientIP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	if !a.config.TrustProxyHeaders || !net.ParseIP(ip).IsLoopback() {
		return ip
	}
	forwarded := r.Header.Values("X-Forwarded-For")
	for i := len(forwarded) - 1; i >= 0; i-- {
		addresses := strings.Split(forwarded[i], ",")
		for j := len(addresses) - 1; j >= 0; j-- {
			if addr := net.ParseIP(strings.TrimSpace(addresses[j])); addr != nil {
				return addr.String()
			}
		}
	}
	if addr := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); addr != nil {
		return addr.String()
	}
	return ip
}

type loginRequest struct {
	Username   string `json:"username"`
	Password   string `json:"password"`
	DeviceName string `json:"deviceName"`
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	if !a.logins.allow("ip:"+a.clientIP(r), 10) {
		loginRateLimited(w)
		return
	}
	var req loginRequest
	if !decode(w, r, &req) {
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	req.DeviceName = strings.TrimSpace(req.DeviceName)
	if identity.ValidUsername(req.Username) && !a.logins.allow("user:"+req.Username, 5) {
		loginRateLimited(w)
		return
	}
	if !identity.ValidUsername(req.Username) || !identity.ValidPassword(req.Password) || !identity.ValidName(req.DeviceName, 80) {
		fail(w, http.StatusBadRequest, "invalid_login", "Provide a username, password and device name.")
		return
	}
	user, hash, err := a.db.Credentials(r.Context(), req.Username)
	missing := errors.Is(err, sql.ErrNoRows)
	if err != nil && !missing {
		a.internal(w, r, err)
		return
	}
	if missing {
		hash = dummyHash
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(req.Password)) != nil || missing {
		fail(w, http.StatusUnauthorized, "invalid_credentials", "Invalid username or password.")
		return
	}
	if !user.IsActive {
		inactive(w)
		return
	}
	device, token, err := a.db.IssueDeviceForLogin(r.Context(), user.ID, req.DeviceName, hash)
	switch {
	case errors.Is(err, store.ErrInactiveUser):
		inactive(w)
		return
	case errors.Is(err, sql.ErrNoRows):
		fail(w, http.StatusUnauthorized, "invalid_credentials", "Invalid username or password.")
		return
	case err != nil:
		a.internal(w, r, err)
		return
	}
	replySession(w, http.StatusOK, user, device, token)
}

func (a *API) register(w http.ResponseWriter, r *http.Request) {
	if !a.logins.allow("register:"+a.clientIP(r), 5) {
		loginRateLimited(w)
		return
	}
	var req loginRequest
	if !decode(w, r, &req) {
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	req.DeviceName = strings.TrimSpace(req.DeviceName)
	if !identity.ValidUsername(req.Username) || !identity.ValidPassword(req.Password) || !identity.ValidName(req.DeviceName, 80) {
		fail(w, http.StatusBadRequest, "invalid_registration", "Provide a valid username, password and device name.")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	user, device, token, err := a.db.Register(r.Context(), req.Username, hash, req.DeviceName)
	switch {
	case errors.Is(err, store.ErrUsernameTaken):
		fail(w, http.StatusConflict, "username_taken", "Username is already taken.")
	case err != nil:
		a.internal(w, r, err)
	default:
		replySession(w, http.StatusCreated, user, device, token)
	}
}

func replySession(w http.ResponseWriter, status int, user store.User, device store.Device, token string) {
	reply(w, status, struct {
		session
		Token string `json:"token"`
	}{session{user, device}, token})
}

func loginRateLimited(w http.ResponseWriter) {
	fail(w, http.StatusTooManyRequests, "rate_limited", "Too many login attempts. Try again later.")
}

func bearerToken(r *http.Request) string {
	scheme, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	if !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

func (a *API) currentQuota(w http.ResponseWriter, r *http.Request, session session) {
	quota, err := a.db.CurrentQuota(r.Context(), session.User)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	reply(w, http.StatusOK, quota)
}

func (a *API) withAuth(next func(http.ResponseWriter, *http.Request, session)) http.HandlerFunc {
	return a.withPendingAuth(func(w http.ResponseWriter, r *http.Request, s session) {
		if s.User.Role == "pending" {
			pendingAccount(w)
			return
		}
		next(w, r, s)
	})
}

func (a *API) withPendingAuth(next func(http.ResponseWriter, *http.Request, session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, err := a.authenticate(r.Context(), bearerToken(r))
		switch {
		case errors.Is(err, sql.ErrNoRows):
			unauthorized(w)
		case errors.Is(err, store.ErrInactiveUser):
			inactive(w)
		case err != nil:
			a.internal(w, r, err)
		default:
			next(w, r, s)
		}
	}
}

func (a *API) authenticate(ctx context.Context, token string) (session, error) {
	if len(token) != 64 {
		return session{}, sql.ErrNoRows
	}
	if _, err := hex.DecodeString(token); err != nil {
		return session{}, sql.ErrNoRows
	}
	user, device, err := a.db.Authenticate(ctx, token)
	return session{user, device}, err
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	fail(w, http.StatusUnauthorized, "unauthorized", "A valid device token is required.")
}

func pendingAccount(w http.ResponseWriter) {
	fail(w, http.StatusForbidden, "account_pending", "Account is awaiting approval.")
}

func inactive(w http.ResponseWriter) {
	fail(w, http.StatusForbidden, "account_inactive", "Account is inactive.")
}

func (a *API) me(w http.ResponseWriter, _ *http.Request, s session) {
	reply(w, http.StatusOK, s)
}

func (a *API) logout(w http.ResponseWriter, r *http.Request, s session) {
	if err := a.db.DeleteDeviceByToken(r.Context(), bearerToken(r)); err != nil {
		a.internal(w, r, err)
		return
	}
	a.live.disconnectDevice(s.User.ID, s.Device.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) devices(w http.ResponseWriter, r *http.Request, s session) {
	devices, err := a.db.UserDevices(r.Context(), s.User.ID)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	type listedDevice struct {
		store.Device
		IsCurrent bool `json:"isCurrent"`
	}
	result := make([]listedDevice, 0, len(devices))
	for _, device := range devices {
		result = append(result, listedDevice{device, device.ID == s.Device.ID})
	}
	reply(w, http.StatusOK, result)
}

func (a *API) deleteDevice(w http.ResponseWriter, r *http.Request, s session) {
	deviceID := r.PathValue("id")
	switch err := a.db.DeleteDeviceByID(r.Context(), s.User.ID, deviceID); {
	case errors.Is(err, sql.ErrNoRows):
		fail(w, http.StatusNotFound, "device_not_found", "Device not found.")
	case err != nil:
		a.internal(w, r, err)
	default:
		a.live.disconnectDevice(s.User.ID, deviceID)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *API) directoryUsers(w http.ResponseWriter, r *http.Request, _ session) {
	users, err := a.db.DirectoryUsers(r.Context())
	if err != nil {
		a.internal(w, r, err)
		return
	}
	reply(w, http.StatusOK, users)
}

func (a *API) putChatShare(w http.ResponseWriter, r *http.Request, s session) {
	var req struct {
		Access string `json:"access"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !store.ValidShareAccess(req.Access) {
		fail(w, http.StatusBadRequest, "invalid_share", "Выберите права доступа к чату.")
		return
	}
	a.changeChatShare(w, r, s, req.Access)
}

func (a *API) deleteChatShare(w http.ResponseWriter, r *http.Request, s session) {
	a.changeChatShare(w, r, s, "")
}

func (a *API) changeChatShare(w http.ResponseWriter, r *http.Request, s session, access string) {
	chatID, targetID := r.PathValue("id"), r.PathValue("userId")
	err := a.live.changeShare(r.Context(), s.User.ID, chatID, targetID, access)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		fail(w, http.StatusNotFound, "chat_not_found", "Chat not found.")
	case errors.Is(err, errShareDuringRun):
		fail(w, http.StatusConflict, "chat_busy", "Дождитесь окончания ответа, чтобы открыть доступ к чату.")
	case errors.Is(err, store.ErrInvalidShare):
		fail(w, http.StatusBadRequest, "invalid_share", "Выберите другого активного пользователя.")
	case err != nil:
		a.internal(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *API) forkChat(w http.ResponseWriter, r *http.Request, s session) {
	chat, err := a.db.ForkChat(r.Context(), s.User.ID, r.PathValue("id"))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		fail(w, http.StatusNotFound, "chat_not_found", "Chat not found.")
	case err != nil:
		a.internal(w, r, err)
	default:
		reply(w, http.StatusCreated, chat)
	}
}

func (a *API) chats(w http.ResponseWriter, r *http.Request, s session) {
	chats, err := a.db.Chats(r.Context(), s.User.ID)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	reply(w, http.StatusOK, chats)
}

func (a *API) createChat(w http.ResponseWriter, r *http.Request, s session) {
	var req struct {
		Title string `json:"title"`
		Model string `json:"model"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	req.Model = strings.TrimSpace(req.Model)
	if req.Title == "" {
		req.Title = "Новый чат"
	}
	if req.Model == "" {
		req.Model = "default"
	}
	if !identity.ValidName(req.Title, 200) || !identity.ValidName(req.Model, 128) {
		fail(w, http.StatusBadRequest, "invalid_chat", "Invalid chat title or model.")
		return
	}
	c, err := a.db.CreateChat(r.Context(), s.User.ID, req.Title, a.config.Provider, req.Model)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	reply(w, http.StatusCreated, c)
}

func (a *API) messages(w http.ResponseWriter, r *http.Request, s session) {
	messages, err := a.db.Messages(r.Context(), s.User.ID, r.PathValue("id"))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		fail(w, http.StatusNotFound, "chat_not_found", "Chat not found.")
	case err != nil:
		a.internal(w, r, err)
	default:
		reply(w, http.StatusOK, messages)
	}
}

func decode[T any](w http.ResponseWriter, r *http.Request, dst *T) bool {
	return decodeLimit(w, r, dst, 8<<10)
}

func decodeLimit[T any](w http.ResponseWriter, r *http.Request, dst *T, limit int64) bool {
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType != "application/json" {
		fail(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Expected application/json.")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var value *T
	err := decoder.Decode(&value)
	if err == nil && value != nil {
		var extra any
		if err = decoder.Decode(&extra); err == io.EOF {
			*dst = *value
			return true
		}
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		fail(w, http.StatusRequestEntityTooLarge, "body_too_large", "Request body is too large.")
	} else {
		fail(w, http.StatusBadRequest, "invalid_json", "Expected one JSON object.")
	}
	return false
}

func (a *API) internal(w http.ResponseWriter, r *http.Request, err error) {
	a.logger.Error("request failed", "method", r.Method, "pattern", r.Pattern, "error", err)
	fail(w, http.StatusInternalServerError, "internal_error", "Internal server error.")
}

func fail(w http.ResponseWriter, status int, code, message string) {
	reply(w, status, map[string]string{"code": code, "message": message})
}

func reply(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
