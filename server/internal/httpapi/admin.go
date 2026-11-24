package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"agent-console/server/internal/identity"
	"agent-console/server/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func (a *API) RequireAdmin(next func(http.ResponseWriter, *http.Request, session)) http.HandlerFunc {
	return a.withAuth(func(w http.ResponseWriter, r *http.Request, s session) {
		if s.User.Role != "admin" {
			fail(w, http.StatusForbidden, "admin_required", "Administrator access is required.")
			return
		}
		next(w, r, s)
	})
}

func (a *API) adminUsers(w http.ResponseWriter, r *http.Request, _ session) {
	users, err := a.db.AdminUsers(r.Context())
	if err != nil {
		a.internal(w, r, err)
		return
	}
	reply(w, http.StatusOK, users)
}

func (a *API) adminCreateUser(w http.ResponseWriter, r *http.Request, _ session) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if !decode(w, r, &req) {
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if !identity.ValidUsername(req.Username) || !identity.ValidPassword(req.Password) || !store.ValidRole(req.Role) {
		fail(w, http.StatusBadRequest, "invalid_user", "Provide a valid username, password and role.")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	user, err := a.db.CreateUserWithRole(r.Context(), req.Username, hash, req.Role)
	switch {
	case errors.Is(err, store.ErrUsernameTaken):
		fail(w, http.StatusConflict, "username_taken", "Username is already taken.")
	case err != nil:
		a.internal(w, r, err)
	default:
		reply(w, http.StatusCreated, user)
	}
}

func (a *API) adminUpdateUser(w http.ResponseWriter, r *http.Request, s session) {
	var req struct {
		Role        *string `json:"role"`
		IsActive    *bool   `json:"isActive"`
		NewPassword *string `json:"newPassword"`
	}
	if !decode(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	block := req.IsActive != nil && !*req.IsActive
	if id == s.User.ID && ((req.Role != nil && *req.Role != s.User.Role) || block) {
		fail(w, http.StatusBadRequest, "self_edit_forbidden", "You cannot change your own role or deactivate your account.")
		return
	}
	if (req.Role == nil && req.IsActive == nil && req.NewPassword == nil) ||
		(req.Role != nil && !store.ValidRole(*req.Role)) ||
		(req.NewPassword != nil && !identity.ValidPassword(*req.NewPassword)) {
		fail(w, http.StatusBadRequest, "invalid_user_update", "Provide a valid role, active status or new password.")
		return
	}
	var hash []byte
	if req.NewPassword != nil {
		var err error
		hash, err = bcrypt.GenerateFromPassword([]byte(*req.NewPassword), bcrypt.DefaultCost)
		if err != nil {
			a.internal(w, r, err)
			return
		}
	}
	if err := a.db.UpdateAdminUser(r.Context(), id, req.Role, req.IsActive, hash); err != nil {
		a.adminUserError(w, r, err)
		return
	}
	if block || hash != nil {
		a.live.disconnectUser(id)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) adminRevokeDevices(w http.ResponseWriter, r *http.Request, _ session) {
	id := r.PathValue("id")
	if err := a.db.RevokeUserDevices(r.Context(), id); err != nil {
		a.adminUserError(w, r, err)
		return
	}
	a.live.disconnectUser(id)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) adminUserError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		fail(w, http.StatusNotFound, "user_not_found", "User not found.")
		return
	}
	a.internal(w, r, err)
}
