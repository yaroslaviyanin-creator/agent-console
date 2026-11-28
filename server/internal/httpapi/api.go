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
	"strconv"
	"strings"
	"time"

	"agent-console/server/internal/config"
	"agent-console/server/internal/identity"
	"agent-console/server/internal/provider"
	"agent-console/server/internal/store"
	"agent-console/server/internal/webfetch"
	"golang.org/x/crypto/bcrypt"
)

type API struct {
	defaultProvider string
	memoryProvider  string
	db              *store.Store
	config          config.Config
	logger          *slog.Logger
	live            *liveRuns
	catalog         modelCatalog
	index           indexedHistory
	logins          *rateLimiter
	uploads         *rateLimiter
	webfetcher      *webfetch.Fetcher
}

type session struct {
	User   store.User   `json:"user"`
	Device store.Device `json:"device"`
}

func New(ctx context.Context, db *store.Store, cfg config.Config, logger *slog.Logger) (http.Handler, error) {
	defaultID := cfg.DefaultProvider
	if defaultID == "" {
		defaultID = cfg.Provider
	}
	enabled := cfg.EnabledProviders
	if len(enabled) == 0 {
		enabled = []string{defaultID}
	}
	registry := make(map[string]provider.Provider, len(enabled))
	var remote *provider.RemoteRunnerProvider
	for _, id := range enabled {
		if id == "paid_api" {
			return nil, fmt.Errorf("provider %q is not implemented yet", id)
		}
		if !config.IsCanonicalProvider(id) {
			return nil, fmt.Errorf("unsupported provider %q", id)
		}
		if cfg.RunnerURL != "" {
			if remote == nil {
				var err error
				remote, err = provider.NewRemoteRunnerProvider(cfg.RunnerURL, cfg.RunnerSharedSecret)
				if err != nil {
					return nil, fmt.Errorf("configure runner: %w", err)
				}
			}
			registry[id] = remote.ForProvider(id)
		} else if id == "stub" {
			registry[id] = provider.StubProvider{Delay: 100 * time.Millisecond}
		} else {
			return nil, errors.New("RUNNER_URL is required for CLI providers")
		}
	}
	if registry[defaultID] == nil || (cfg.MemoryProvider != "" && registry[cfg.MemoryProvider] == nil) {
		return nil, errors.New("default and memory providers must be enabled")
	}
	cfg.DefaultProvider = defaultID
	return newRegistryHandler(ctx, db, cfg, logger, registry), nil
}

func newHandler(ctx context.Context, db *store.Store, cfg config.Config, logger *slog.Logger, backend provider.Provider) http.Handler {
	if cfg.DefaultProvider == "" {
		cfg.DefaultProvider = cfg.Provider
		if cfg.DefaultProvider == "" {
			cfg.DefaultProvider = "stub"
		}
	}
	return newRegistryHandler(ctx, db, cfg, logger, map[string]provider.Provider{cfg.DefaultProvider: backend})
}

func newRegistryHandler(ctx context.Context, db *store.Store, cfg config.Config, logger *slog.Logger, registry map[string]provider.Provider) http.Handler {
	memoryID := cfg.MemoryProvider
	if memoryID == "" {
		memoryID = cfg.DefaultProvider
	}
	api := &API{db: db, config: cfg, logger: logger, defaultProvider: cfg.DefaultProvider, memoryProvider: memoryID,
		live:    newLiveRunsWithProviders(ctx, cfg.DefaultProvider, registry, max(1, cfg.MaxConcurrentStreams)),
		logins:     newRateLimiter(),
		uploads:    newRateLimiter(),
		webfetcher: webfetch.NewFetcher()}
	api.live.db = db
	api.live.logger = logger
	if db != nil {
		db.SetSessionTTL(cfg.EffectiveSessionTTL())
	}
	if db != nil && cfg.MemoryAutoUpdate && cfg.MemoryCheckInterval > 0 {
		go api.runMemoryWorker(ctx)
	}
	if db != nil {
		go api.runAttachmentCleanupWorker(ctx)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", api.health)
	mux.HandleFunc("POST /api/auth/login", api.login)
	mux.HandleFunc("POST /api/auth/register", api.register)
	mux.HandleFunc("POST /api/auth/telegram/verify", api.verifyTelegramInitData)
	mux.HandleFunc("GET /api/auth/me", api.withPendingAuth(api.me))
	mux.HandleFunc("POST /api/auth/logout", api.withPendingAuth(api.logout))
	mux.HandleFunc("POST /api/auth/logout-all", api.withPendingAuth(api.logoutAll))
	mux.HandleFunc("GET /api/devices", api.withPendingAuth(api.devices))
	mux.HandleFunc("DELETE /api/devices/{id}", api.withPendingAuth(api.deleteDevice))
	mux.HandleFunc("POST /api/ws/ticket", api.withAuth(api.issueWSTicket))

	adminMux := http.NewServeMux()
	adminMux.HandleFunc("GET /api/admin/users/{id}/chats", api.RequireAdmin(api.adminUserChats))
	adminMux.HandleFunc("GET /api/admin/users/{id}/chats/{chatId}/messages", api.RequireAdmin(api.adminChatMessages))
	adminMux.HandleFunc("GET /api/admin/users", api.RequireAdmin(api.adminUsers))
	adminMux.HandleFunc("POST /api/admin/users", api.RequireAdmin(api.adminCreateUser))
	adminMux.HandleFunc("PATCH /api/admin/users/{id}", api.RequireAdmin(api.adminUpdateUser))
	adminMux.HandleFunc("DELETE /api/admin/users/{id}", api.RequireAdmin(api.adminDeleteUser))
	adminMux.HandleFunc("DELETE /api/admin/users/{id}/devices", api.RequireAdmin(api.adminRevokeDevices))
	mux.Handle("/api/admin", api.adminGuard(adminMux))
	mux.Handle("/api/admin/", api.adminGuard(adminMux))
	mux.HandleFunc("GET /api/codex/chats", api.withAuth(api.indexedChats))
	mux.HandleFunc("GET /api/codex/chats/{id}/messages", api.withAuth(api.indexedMessages))
	mux.HandleFunc("GET /api/quotas/current", api.withAuth(api.currentQuota))
	mux.HandleFunc("GET /api/profile", api.withAuth(api.getProfile))
	mux.HandleFunc("PATCH /api/profile", api.withAuth(api.patchProfile))
	mux.HandleFunc("GET /api/models", api.withAuth(api.models))
	mux.HandleFunc("GET /api/providers", api.withAuth(api.providers))
	mux.HandleFunc("GET /api/users", api.withAuth(api.directoryUsers))
	mux.HandleFunc("GET /api/chats", api.withAuth(api.chats))
	mux.HandleFunc("POST /api/chats", api.withAuth(api.createChat))
	mux.HandleFunc("PATCH /api/chats/{id}", api.withAuth(api.patchChat))
	mux.HandleFunc("DELETE /api/chats/{id}", api.withAuth(api.deleteChat))
	mux.HandleFunc("GET /api/folders", api.withAuth(api.listFolders))
	mux.HandleFunc("POST /api/folders", api.withAuth(api.createFolder))
	mux.HandleFunc("PATCH /api/folders/{id}", api.withAuth(api.patchFolder))
	mux.HandleFunc("DELETE /api/folders/{id}", api.withAuth(api.deleteFolder))
	mux.HandleFunc("GET /api/search", api.withAuth(api.searchMessages))
	mux.HandleFunc("POST /api/chats/{id}/attachments", api.withAuth(api.uploadAttachments))
	mux.HandleFunc("GET /api/attachments/{id}", api.withAuth(api.getAttachment))
	mux.HandleFunc("DELETE /api/attachments/{id}", api.withAuth(api.deleteAttachment))
	mux.HandleFunc("GET /api/chats/{id}/messages", api.withAuth(api.messages))
	mux.HandleFunc("DELETE /api/chats/{id}/messages/{messageId}", api.withAuth(api.deleteMessage))
	mux.HandleFunc("POST /api/chats/{id}/messages/{messageId}/regenerate", api.withAuth(api.regenerateMessage))
	mux.HandleFunc("PATCH /api/chats/{id}/messages/{messageId}", api.withAuth(api.editMessage))
	mux.HandleFunc("PUT /api/chats/{id}/shares/{userId}", api.withAuth(api.putChatShare))
	mux.HandleFunc("DELETE /api/chats/{id}/shares/{userId}", api.withAuth(api.deleteChatShare))
	mux.HandleFunc("POST /api/chats/{id}/fork", api.withAuth(api.forkChat))
	mux.HandleFunc("GET /ws", api.websocket)
	return api.cors(mux)
}

func (a *API) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
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

func (a *API) health(w http.ResponseWriter, _ *http.Request) {
	reply(w, http.StatusOK, map[string]string{"status": "ok", "provider": a.defaultProvider})
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
	if allowed, retryAfter := a.logins.allowWindow("ip:"+a.clientIP(r), 10, time.Minute); !allowed {
		loginRateLimited(w, retryAfter)
		return
	}
	var req loginRequest
	if !decode(w, r, &req) {
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	req.DeviceName = strings.TrimSpace(req.DeviceName)
	if identity.ValidUsername(req.Username) {
		if allowed, retryAfter := a.logins.allowWindow("user:"+req.Username, 5, time.Minute); !allowed {
			loginRateLimited(w, retryAfter)
			return
		}
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
	if allowed, retryAfter := a.logins.allowWindow("register:"+a.clientIP(r), 5, time.Minute); !allowed {
		loginRateLimited(w, retryAfter)
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
	case user.Role == "pending" || token == "":
		reply(w, http.StatusCreated, struct {
			User    store.User `json:"user"`
			Pending bool       `json:"pending"`
		}{User: user, Pending: true})
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

func loginRateLimited(w http.ResponseWriter, retryAfter int) {
	w.Header().Set("Retry-After", strconv.Itoa(max(1, retryAfter)))
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

func (a *API) adminGuard(next http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, err := a.authenticate(r.Context(), bearerToken(r))
		if err != nil || s.User.Role != "admin" {
			fail(w, http.StatusNotFound, "not_found", "Not found")
			return
		}
		next.ServeHTTP(w, r)
	}
}

func (a *API) logout(w http.ResponseWriter, r *http.Request, s session) {
	if err := a.db.DeleteDeviceByToken(r.Context(), bearerToken(r)); err != nil {
		a.internal(w, r, err)
		return
	}
	a.live.disconnectDevice(s.User.ID, s.Device.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) logoutAll(w http.ResponseWriter, r *http.Request, s session) {
	var req struct {
		IncludeCurrent bool `json:"includeCurrent"`
	}
	if r.ContentLength != 0 {
		if !decode(w, r, &req) {
			return
		}
	}
	if req.IncludeCurrent {
		if err := a.db.RevokeUserDevices(r.Context(), s.User.ID); err != nil {
			a.internal(w, r, err)
			return
		}
		a.live.disconnectUser(s.User.ID)
	} else {
		devices, err := a.db.UserDevices(r.Context(), s.User.ID)
		if err != nil {
			a.internal(w, r, err)
			return
		}
		if err := a.db.RevokeOtherDevices(r.Context(), s.User.ID, s.Device.ID); err != nil {
			a.internal(w, r, err)
			return
		}
		for _, d := range devices {
			if d.ID != s.Device.ID {
				a.live.disconnectDevice(s.User.ID, d.ID)
			}
		}
	}
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

func (a *API) directoryUsers(w http.ResponseWriter, r *http.Request, s session) {
	q := r.URL.Query().Get("q")
	limit := 10
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil {
			limit = parsed
		}
	}
	users, err := a.db.DirectoryUsers(r.Context(), s.User.ID, q, limit)
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
	var req struct {
		UpToMessageID string `json:"upToMessageId"`
	}
	if r.ContentLength != 0 && r.Header.Get("Content-Type") != "" {
		if !decode(w, r, &req) {
			return
		}
	}
	upTo := strings.TrimSpace(req.UpToMessageID)
	if upTo != "" && !correlationID.MatchString(upTo) {
		fail(w, http.StatusBadRequest, "invalid_message", "Invalid message ID.")
		return
	}
	chat, err := a.db.ForkChatUpTo(r.Context(), s.User.ID, r.PathValue("id"), upTo)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		fail(w, http.StatusNotFound, "chat_not_found", "Chat not found.")
	case err != nil:
		a.internal(w, r, err)
	default:
		reply(w, http.StatusCreated, chat)
	}
}

func (a *API) patchChat(w http.ResponseWriter, r *http.Request, s session) {
	var req struct {
		Title    *string          `json:"title"`
		Pinned   *bool            `json:"pinned"`
		FolderID *json.RawMessage `json:"folderId"`
		Archived *bool            `json:"archived"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Title == nil && req.Pinned == nil && req.FolderID == nil && req.Archived == nil {
		fail(w, http.StatusBadRequest, "invalid_chat", "Provide title, pinned, folderId, or archived.")
		return
	}
	if req.Title != nil {
		trimmed := strings.TrimSpace(*req.Title)
		if !identity.ValidName(trimmed, 200) {
			fail(w, http.StatusBadRequest, "invalid_chat", "Invalid chat title.")
			return
		}
		req.Title = &trimmed
	}
	var (
		folderSet bool
		folderVal *string
	)
	if req.FolderID != nil {
		folderSet = true
		raw := strings.TrimSpace(string(*req.FolderID))
		if raw != "" && raw != "null" {
			var parsed string
			if err := json.Unmarshal(*req.FolderID, &parsed); err != nil {
				fail(w, http.StatusBadRequest, "invalid_chat", "Invalid folderId.")
				return
			}
			parsed = strings.TrimSpace(parsed)
			if parsed != "" {
				if !correlationID.MatchString(parsed) {
					fail(w, http.StatusBadRequest, "invalid_chat", "Invalid folderId.")
					return
				}
				folderVal = &parsed
			}
		}
	}
	chat, err := a.db.UpdateChatWithOptions(r.Context(), s.User.ID, r.PathValue("id"), req.Title, req.Pinned, folderSet, folderVal, req.Archived)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		fail(w, http.StatusNotFound, "chat_not_found", "Chat not found.")
	case errors.Is(err, store.ErrForbidden):
		fail(w, http.StatusForbidden, "forbidden", "Access denied.")
	case err != nil:
		a.internal(w, r, err)
	default:
		reply(w, http.StatusOK, chat)
	}
}

func (a *API) isChatBusy(chatID string) bool {
	a.live.mu.Lock()
	defer a.live.mu.Unlock()
	_, busy := a.live.chats[chatID]
	return busy
}

func (a *API) deleteMessage(w http.ResponseWriter, r *http.Request, s session) {
	chatID := r.PathValue("id")
	if a.isChatBusy(chatID) {
		fail(w, http.StatusConflict, "chat_busy", "Дождитесь окончания ответа перед изменением истории.")
		return
	}
	err := a.db.DeleteMessage(r.Context(), s.User.ID, chatID, r.PathValue("messageId"))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		fail(w, http.StatusNotFound, "message_not_found", "Message not found.")
	case errors.Is(err, store.ErrForbidden):
		fail(w, http.StatusForbidden, "forbidden", "Access denied.")
	case err != nil:
		a.internal(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *API) regenerateMessage(w http.ResponseWriter, r *http.Request, s session) {
	chatID := r.PathValue("id")
	if a.isChatBusy(chatID) {
		fail(w, http.StatusConflict, "chat_busy", "Дождитесь окончания ответа перед перегенерацией.")
		return
	}
	var req struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
	}
	if r.ContentLength != 0 && r.Header.Get("Content-Type") != "" {
		if !decode(w, r, &req) {
			return
		}
	}
	targetProvider := strings.ToLower(strings.TrimSpace(req.Provider))
	targetModel := strings.TrimSpace(req.Model)
	if targetProvider != "" {
		if targetProvider == "paid_api" || !a.live.hasProvider(targetProvider) {
			fail(w, http.StatusUnprocessableEntity, "provider_unavailable", "Requested provider is not available.")
			return
		}
	}
	if targetModel != "" && !modelID.MatchString(targetModel) {
		fail(w, http.StatusBadRequest, "invalid_model", "Invalid model ID.")
		return
	}
	userMsgID, prompt, attachments, err := a.db.PrepareRegenerate(r.Context(), s.User.ID, chatID, r.PathValue("messageId"), targetProvider, targetModel)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		fail(w, http.StatusNotFound, "message_not_found", "Message not found.")
	case errors.Is(err, store.ErrForbidden):
		fail(w, http.StatusForbidden, "forbidden", "Access denied.")
	case err != nil:
		a.internal(w, r, err)
	default:
		wireAtts := make([]store.MessageAttachment, 0, len(attachments))
		for _, att := range attachments {
			wireAtts = append(wireAtts, att.AsMessageAttachment())
		}
		reply(w, http.StatusOK, map[string]any{
			"userMessageId": userMsgID,
			"prompt":        prompt,
			"attachments":   wireAtts,
		})
	}
}

func (a *API) editMessage(w http.ResponseWriter, r *http.Request, s session) {
	chatID := r.PathValue("id")
	if a.isChatBusy(chatID) {
		fail(w, http.StatusConflict, "chat_busy", "Дождитесь окончания ответа перед редактированием.")
		return
	}
	var req struct {
		Content string `json:"content"`
	}
	if !decodeLimit(w, r, &req, 64<<10) {
		return
	}
	req.Content = strings.TrimSpace(req.Content)
	if req.Content == "" || len(req.Content) > 32<<10 || strings.ContainsRune(req.Content, 0) {
		fail(w, http.StatusBadRequest, "invalid_message", "Invalid message content.")
		return
	}
	userMsgID, attachments, err := a.db.PrepareEditMessage(r.Context(), s.User.ID, chatID, r.PathValue("messageId"), req.Content)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		fail(w, http.StatusNotFound, "message_not_found", "Message not found.")
	case errors.Is(err, store.ErrForbidden):
		fail(w, http.StatusForbidden, "forbidden", "Access denied.")
	case err != nil:
		a.internal(w, r, err)
	default:
		wireAtts := make([]store.MessageAttachment, 0, len(attachments))
		for _, att := range attachments {
			wireAtts = append(wireAtts, att.AsMessageAttachment())
		}
		reply(w, http.StatusOK, map[string]any{
			"userMessageId": userMsgID,
			"prompt":        req.Content,
			"attachments":   wireAtts,
		})
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

func (a *API) providers(w http.ResponseWriter, _ *http.Request, _ session) {
	type entry struct {
		ID        string `json:"id"`
		Available bool   `json:"available"`
	}
	items := make([]entry, 0, 5)
	for _, id := range []string{"antigravity", "codex", "claude", "paid_api"} {
		items = append(items, entry{id, a.live.hasProvider(id)})
	}
	if a.live.hasProvider("stub") {
		items = append(items, entry{"stub", true})
	}
	reply(w, http.StatusOK, map[string]any{"defaultProvider": a.defaultProvider, "providers": items})
}

func (a *API) createChat(w http.ResponseWriter, r *http.Request, s session) {
	var req struct {
		Title    string `json:"title"`
		Model    string `json:"model"`
		Provider string `json:"provider"`
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
	providerID := strings.ToLower(strings.TrimSpace(req.Provider))
	if providerID == "" {
		providerID = a.defaultProvider
	}
	if !a.live.hasProvider(providerID) {
		fail(w, http.StatusUnprocessableEntity, "provider_unavailable", "Requested provider is not available.")
		return
	}
	c, err := a.db.CreateChat(r.Context(), s.User.ID, req.Title, providerID, req.Model)
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
		for i := range messages {
			if messages[i].Role == "assistant" && messages[i].Provider != "" && isDefaultModelPlaceholder(messages[i].Model, messages[i].Provider) {
				if resolved := a.resolveDefaultModelID(r.Context(), messages[i].Provider); resolved != "" {
					messages[i].Model = resolved
				}
			}
		}
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
