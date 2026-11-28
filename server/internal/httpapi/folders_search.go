package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"agent-console/server/internal/identity"
	"agent-console/server/internal/store"
	"agent-console/server/internal/uploads"
)

var folderHexColorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

const (
	defaultFolderColor           = "#6366f1"
	maxProjectSystemPromptBytes  = 8192
	maxFolderAttachmentsPerFolder = 20
)

func validProjectSystemPrompt(raw string) bool {
	return len(raw) <= maxProjectSystemPromptBytes && utf8.ValidString(raw) && !strings.ContainsRune(raw, 0)
}

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
		Name         string `json:"name"`
		Color        string `json:"color"`
		SystemPrompt string `json:"systemPrompt"`
	}
	if !decodeLimit(w, r, &req, 16<<10) {
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
	sysPrompt := strings.TrimSpace(req.SystemPrompt)
	if !validProjectSystemPrompt(sysPrompt) {
		fail(w, http.StatusBadRequest, "invalid_folder", "Инструкции проекта не должны превышать 8 КБ.")
		return
	}
	folder, err := a.db.CreateFolder(r.Context(), s.User.ID, name, color, sysPrompt)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	reply(w, http.StatusCreated, folder)
}

func (a *API) patchFolder(w http.ResponseWriter, r *http.Request, s session) {
	var req struct {
		Name         *string `json:"name"`
		Color        *string `json:"color"`
		SortOrder    *int    `json:"sortOrder"`
		SystemPrompt *string `json:"systemPrompt"`
	}
	if !decodeLimit(w, r, &req, 16<<10) {
		return
	}
	if req.Name == nil && req.Color == nil && req.SortOrder == nil && req.SystemPrompt == nil {
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
	if req.SystemPrompt != nil {
		trimmed := strings.TrimSpace(*req.SystemPrompt)
		if !validProjectSystemPrompt(trimmed) {
			fail(w, http.StatusBadRequest, "invalid_folder", "Инструкции проекта не должны превышать 8 КБ.")
			return
		}
		req.SystemPrompt = &trimmed
	}
	folder, err := a.db.UpdateFolder(r.Context(), s.User.ID, r.PathValue("id"), req.Name, req.Color, req.SortOrder, req.SystemPrompt)
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

func (a *API) listFolderAttachments(w http.ResponseWriter, r *http.Request, s session) {
	atts, err := a.db.ListFolderAttachments(r.Context(), s.User.ID, r.PathValue("id"))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		fail(w, http.StatusNotFound, "folder_not_found", "Folder not found.")
	case err != nil:
		a.internal(w, r, err)
	default:
		reply(w, http.StatusOK, map[string]any{"attachments": atts})
	}
}

func (a *API) uploadFolderAttachments(w http.ResponseWriter, r *http.Request, s session) {
	if !a.allowUserAndIPRate(w, r, a.uploads, "folder_upload", s.User.ID, chatUploadUserRateLimit, chatUploadIPRateLimit, time.Minute) {
		return
	}
	folderID := r.PathValue("id")
	if _, err := a.db.GetFolder(r.Context(), s.User.ID, folderID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			fail(w, http.StatusNotFound, "folder_not_found", "Folder not found.")
			return
		}
		a.internal(w, r, err)
		return
	}

	mediaType, params, ctErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if ctErr != nil || mediaType != "multipart/form-data" || strings.TrimSpace(params["boundary"]) == "" {
		fail(w, http.StatusBadRequest, "invalid_multipart", "Ожидается multipart/form-data с файлами.")
		return
	}

	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(10 * time.Minute))
	_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Minute))

	maxFileBytes := a.config.EffectiveMaxAttachmentBytes()
	maxRequestBytes := maxFileBytes + maxChatUploadMultipartOverheadBytes
	maxPerMessage := a.config.EffectiveMaxAttachmentsPerMessage()
	maxContextBytes := a.config.EffectiveMaxAttachmentContextBytes()

	existingCount, err := a.db.CountFolderAttachments(r.Context(), folderID)
	if err != nil {
		a.internal(w, r, err)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	mr, err := r.MultipartReader()
	if err != nil {
		if errors.Is(err, http.ErrNotMultipart) || errors.Is(err, http.ErrMissingBoundary) {
			fail(w, http.StatusBadRequest, "invalid_multipart", "Ожидается multipart/form-data с файлами.")
			return
		}
		a.internal(w, r, err)
		return
	}

	uploadsDir := a.db.UploadsDir()
	if err := os.MkdirAll(uploadsDir, 0700); err != nil {
		a.internal(w, r, err)
		return
	}

	policy := uploads.DefaultChatAttachmentPolicyWithLimits(maxFileBytes, maxContextBytes)
	created := make([]store.Attachment, 0, maxPerMessage)
	rollbackCreated := func() {
		for _, item := range created {
			_ = a.db.DeleteFolderAttachment(context.Background(), s.User.ID, folderID, item.ID)
		}
	}

	copyBuf := make([]byte, 32<<10)
	streamPartToTemp := func(part io.Reader) (string, int64, error) {
		tmpFile, err := os.CreateTemp(uploadsDir, ".tmp-*")
		if err != nil {
			return "", 0, err
		}
		tmpPath := tmpFile.Name()
		n, copyErr := io.CopyBuffer(tmpFile, io.LimitReader(part, maxFileBytes+1), copyBuf)
		closeErr := tmpFile.Close()
		if copyErr != nil {
			_ = os.Remove(tmpPath)
			return "", 0, copyErr
		}
		if closeErr != nil {
			_ = os.Remove(tmpPath)
			return "", 0, closeErr
		}
		return tmpPath, n, nil
	}

	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			rollbackCreated()
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				fail(w, http.StatusRequestEntityTooLarge, "body_too_large", fmt.Sprintf("Размер загрузки превышает %d МБ.", maxRequestBytes>>20))
				return
			}
			a.internal(w, r, err)
			return
		}
		filename := part.FileName()
		if filename == "" {
			_ = part.Close()
			continue
		}
		if len(created) >= maxPerMessage {
			_ = part.Close()
			rollbackCreated()
			fail(w, http.StatusBadRequest, "too_many_files", fmt.Sprintf("Можно прикрепить не более %d файлов за раз.", maxPerMessage))
			return
		}
		if existingCount+len(created) >= maxFolderAttachmentsPerFolder {
			_ = part.Close()
			rollbackCreated()
			fail(w, http.StatusBadRequest, "too_many_folder_attachments", fmt.Sprintf("В проекте может быть не более %d файлов.", maxFolderAttachmentsPerFolder))
			return
		}

		tmpPath, written, streamErr := streamPartToTemp(part)
		_ = part.Close()
		if streamErr != nil {
			rollbackCreated()
			var tooLarge *http.MaxBytesError
			if errors.As(streamErr, &tooLarge) {
				fail(w, http.StatusRequestEntityTooLarge, "body_too_large", fmt.Sprintf("Размер загрузки превышает %d МБ.", maxRequestBytes>>20))
				return
			}
			a.internal(w, r, streamErr)
			return
		}
		if written == 0 {
			_ = os.Remove(tmpPath)
			rollbackCreated()
			fail(w, http.StatusBadRequest, "empty_file", "Файл пустой и не может быть загружен.")
			return
		}
		if written > maxFileBytes {
			_ = os.Remove(tmpPath)
			rollbackCreated()
			fail(w, http.StatusRequestEntityTooLarge, "attachment_too_large", fmt.Sprintf("Файл превышает допустимый размер (%d МБ).", maxFileBytes>>20))
			return
		}

		validated, err := uploads.ValidateAttachmentFile(r.Context(), policy, filename, tmpPath)
		if err != nil {
			_ = os.Remove(tmpPath)
			rollbackCreated()
			code, msg := mapUploadValidationError(err)
			fail(w, http.StatusBadRequest, code, msg)
			return
		}

		att, err := a.db.CreateFolderAttachment(r.Context(), folderID, s.User.ID, validated)
		if validated.Data != nil {
			_ = os.Remove(tmpPath)
		}
		if err != nil {
			_ = os.Remove(tmpPath)
			rollbackCreated()
			a.internal(w, r, err)
			return
		}
		created = append(created, att)
	}

	if len(created) == 0 {
		fail(w, http.StatusBadRequest, "no_files", "Не передано ни одного файла.")
		return
	}

	reply(w, http.StatusCreated, map[string]any{"attachments": created})
}

func (a *API) getFolderAttachment(w http.ResponseWriter, r *http.Request, s session) {
	att, err := a.db.GetFolderAttachment(r.Context(), s.User.ID, r.PathValue("id"), r.PathValue("attachmentId"))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		fail(w, http.StatusNotFound, "attachment_not_found", "Attachment not found.")
		return
	case err != nil:
		a.internal(w, r, err)
		return
	}

	f, size, err := a.db.OpenAttachmentFile(att.StorageKey)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	defer f.Close()

	inline := att.Kind == string(uploads.KindImage)
	if r.URL.Query().Get("download") == "1" {
		inline = false
	}
	uploads.ApplyDownloadHeaders(w, att.OriginalName, att.MIMEType, uploads.AttachmentKind(att.Kind), inline)
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}

func (a *API) deleteFolderAttachment(w http.ResponseWriter, r *http.Request, s session) {
	err := a.db.DeleteFolderAttachment(r.Context(), s.User.ID, r.PathValue("id"), r.PathValue("attachmentId"))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		fail(w, http.StatusNotFound, "attachment_not_found", "Attachment not found.")
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
