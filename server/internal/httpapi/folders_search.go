package httpapi

import (
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"agent-console/server/internal/identity"
	"agent-console/server/internal/store"
)

var folderHexColorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

const defaultFolderColor = "#6366f1"

func normalizeFolderColor(raw string) (string, bool) {
	c := strings.TrimSpace(raw)
	if c == "" {
		return defaultFolderColor, true
	}
	if !folderHexColorRe.MatchString(c) {
		return "", false
	}
	return strings.ToLower(c), true
}

func (a *API) listFolders(w http.ResponseWriter, r *http.Request, s session) {
	folders, err := a.db.ListFolders(r.Context(), s.User.ID)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	reply(w, http.StatusOK, folders)
}

func (a *API) createFolder(w http.ResponseWriter, r *http.Request, s session) {
	var req struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	}
	if !decode(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if !identity.ValidName(name, 60) {
		fail(w, http.StatusBadRequest, "invalid_folder", "Название папки должно содержать от 1 до 60 символов.")
		return
	}
	color, ok := normalizeFolderColor(req.Color)
	if !ok {
		fail(w, http.StatusBadRequest, "invalid_folder", "Некорректный цвет папки (ожидается #RRGGBB).")
		return
	}
	folder, err := a.db.CreateFolder(r.Context(), s.User.ID, name, color)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	reply(w, http.StatusCreated, folder)
}

func (a *API) patchFolder(w http.ResponseWriter, r *http.Request, s session) {
	var req struct {
		Name      *string `json:"name"`
		Color     *string `json:"color"`
		SortOrder *int    `json:"sortOrder"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Name == nil && req.Color == nil && req.SortOrder == nil {
		fail(w, http.StatusBadRequest, "invalid_folder", "Укажите поля для обновления папки.")
		return
	}
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		if !identity.ValidName(trimmed, 60) {
			fail(w, http.StatusBadRequest, "invalid_folder", "Название папки должно содержать от 1 до 60 символов.")
			return
		}
		req.Name = &trimmed
	}
	if req.Color != nil {
		normalized, ok := normalizeFolderColor(*req.Color)
		if !ok {
			fail(w, http.StatusBadRequest, "invalid_folder", "Некорректный цвет папки (ожидается #RRGGBB).")
			return
		}
		req.Color = &normalized
	}
	folder, err := a.db.UpdateFolder(r.Context(), s.User.ID, r.PathValue("id"), req.Name, req.Color, req.SortOrder)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		fail(w, http.StatusNotFound, "folder_not_found", "Folder not found.")
	case err != nil:
		a.internal(w, r, err)
	default:
		reply(w, http.StatusOK, folder)
	}
}

func (a *API) deleteFolder(w http.ResponseWriter, r *http.Request, s session) {
	err := a.db.DeleteFolder(r.Context(), s.User.ID, r.PathValue("id"))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		fail(w, http.StatusNotFound, "folder_not_found", "Folder not found.")
	case err != nil:
		a.internal(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *API) searchMessages(w http.ResponseWriter, r *http.Request, s session) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		reply(w, http.StatusOK, []store.SearchResult{})
		return
	}
	limit := 30
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil {
			if parsed < 1 {
				parsed = 1
			} else if parsed > 30 {
				parsed = 30
			}
			limit = parsed
		}
	}
	results, err := a.db.SearchMessages(r.Context(), s.User.ID, q, limit)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	reply(w, http.StatusOK, results)
}
