package httpapi

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
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
}

type session struct {
	User   store.User   `json:"user"`
	Device store.Device `json:"device"`
}

func New(ctx context.Context, db *store.Store, cfg config.Config, logger *slog.Logger) http.Handler {
	var backend provider.Provider = provider.StubProvider{Delay: 100 * time.Millisecond}
	if cfg.Provider == "codex" {
		backend = provider.CodexCliProvider{Bin: cfg.CodexBin, WorkspaceRoot: "./data/workspaces"}
	}
	return newHandler(ctx, db, cfg, logger, backend)
}

func newHandler(ctx context.Context, db *store.Store, cfg config.Config, logger *slog.Logger, backend provider.Provider) http.Handler {
	api := &API{db: db, config: cfg, logger: logger, live: newLiveRuns(ctx, backend)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", api.health)
	mux.HandleFunc("POST /api/auth/login", api.login)
	mux.HandleFunc("GET /api/auth/me", api.withAuth(api.me))
	mux.HandleFunc("GET /api/admin/users", api.RequireAdmin(api.adminUsers))
	mux.HandleFunc("POST /api/admin/users", api.RequireAdmin(api.adminCreateUser))
	mux.HandleFunc("PATCH /api/admin/users/{id}", api.RequireAdmin(api.adminUpdateUser))
	mux.HandleFunc("DELETE /api/admin/users/{id}/devices", api.RequireAdmin(api.adminRevokeDevices))
	mux.HandleFunc("GET /api/codex/chats", api.withAuth(api.indexedChats))
	mux.HandleFunc("GET /api/codex/chats/{id}/messages", api.withAuth(api.indexedMessages))
	mux.HandleFunc("GET /api/models", api.withAuth(api.models))
	mux.HandleFunc("GET /api/chats", api.withAuth(api.chats))
	mux.HandleFunc("POST /api/chats", api.withAuth(api.createChat))
	mux.HandleFunc("GET /api/chats/{id}/messages", api.withAuth(api.messages))
	mux.HandleFunc("GET /ws", api.websocket)
	return api.cors(mux)
}

func (a *API) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Add("Vary", "Origin")
		if origin := r.Header.Get("Origin"); origin != "" {
			if a.config.CORSOrigin != "*" && a.config.CORSOrigin != origin {
				fail(w, http.StatusForbidden, "origin_denied", "Origin is not allowed.")
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", a.config.CORSOrigin)
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) health(w http.ResponseWriter, _ *http.Request) {
	reply(w, http.StatusOK, map[string]string{"status": "ok", "provider": a.config.Provider})
}

var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("dummy login hash"), bcrypt.DefaultCost)

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username   string `json:"username"`
		Password   string `json:"password"`
		DeviceName string `json:"deviceName"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	req.DeviceName = strings.TrimSpace(req.DeviceName)
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
	reply(w, http.StatusOK, struct {
		session
		Token string `json:"token"`
	}{session{user, device}, token})
}

func (a *API) withAuth(next func(http.ResponseWriter, *http.Request, session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scheme, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
		token = strings.TrimSpace(token)
		if !strings.EqualFold(scheme, "Bearer") {
			unauthorized(w)
			return
		}
		s, err := a.authenticate(r.Context(), token)
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

func inactive(w http.ResponseWriter) {
	fail(w, http.StatusForbidden, "account_inactive", "Account is inactive.")
}

func (a *API) me(w http.ResponseWriter, _ *http.Request, s session) {
	reply(w, http.StatusOK, s)
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
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType != "application/json" {
		fail(w, http.StatusUnsupportedMediaType, "invalid_content_type", "Expected application/json.")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
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
