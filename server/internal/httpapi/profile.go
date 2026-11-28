package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"agent-console/server/internal/artifacts"
	"agent-console/server/internal/store"
	"agent-console/server/internal/uploads"
)

const (
	maxProfileBodyBytes  = 3 << 20
	profileUserRateLimit = 8
	profileIPRateLimit   = 20
)

// profilePatchRequest is the strict allowlist of fields a user may modify on their own profile.
// Privileged fields (id, userId, role, planTier, status, isAdmin, isActive) are intentionally absent
// and rejected by decodeLimit's DisallowUnknownFields.
type profilePatchRequest struct {
	DisplayName   *string          `json:"displayName"`
	AvatarData    *string          `json:"avatarData"`
	ProfileMD     *string          `json:"profileMd"`
	MemoryMD      *string          `json:"memoryMd"`
	MemoryEnabled *bool            `json:"memoryEnabled"`
	UISettings    *json.RawMessage `json:"uiSettings"`
}

func (a *API) getProfile(w http.ResponseWriter, r *http.Request, s session) {
	profile, err := a.db.UserProfile(r.Context(), s.User.ID)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	reply(w, http.StatusOK, profile)
}

func (a *API) patchProfile(w http.ResponseWriter, r *http.Request, s session) {
	if !a.allowUserAndIPRate(w, r, a.uploads, "profile", s.User.ID, profileUserRateLimit, profileIPRateLimit, time.Minute) {
		return
	}
	var patch profilePatchRequest
	if !decodeLimit(w, r, &patch, maxProfileBodyBytes) {
		return
	}
	if patch.AvatarData != nil {
		sanitized, err := uploads.SanitizeAvatarDataURL(*patch.AvatarData)
		if err != nil {
			fail(w, http.StatusBadRequest, "invalid_avatar", "Аватар должен быть корректным изображением JPEG, PNG, WebP или GIF (до 2 МБ и 4096×4096 px).")
			return
		}
		patch.AvatarData = &sanitized
	}
	profile, err := a.db.UpdateUserProfileWithUI(r.Context(), s.User.ID, patch.DisplayName, patch.ProfileMD, patch.MemoryMD, patch.MemoryEnabled, patch.UISettings, patch.AvatarData)
	switch {
	case errors.Is(err, store.ErrInvalidUISettings):
		fail(w, http.StatusBadRequest, "invalid_ui_settings", "Некорректные настройки оформления (допустимы только известные поля, HEX #RRGGBB, шаблоны и размер до 32 КБ).")
	case errors.Is(err, store.ErrInvalidProfile):
		fail(w, http.StatusBadRequest, "invalid_profile", "Имя должно занимать до 80 символов, аватар — до 2 МБ, а каждый текст — до 4096 байт.")
	case err != nil:
		a.internal(w, r, err)
	default:
		reply(w, http.StatusOK, profile)
	}
}

// userInstructions builds the per-user system context for LLM runs.
// AvatarData and uploaded file metadata are strictly excluded so image metadata cannot inject prompts.
func (a *API) userInstructions(ctx context.Context, userID string, shared bool, projectPrompt string) (string, error) {
	profile, err := a.db.UserProfile(ctx, userID)
	if err != nil {
		return "", err
	}
	name := profile.DisplayName
	if name == "" {
		name, err = a.db.Username(ctx, userID)
		if err != nil {
			return "", err
		}
	}
	if shared {
		return "Текущий собеседник: " + name + "\n\n" + artifacts.ArtifactSystemPrompt, nil
	}

	var instructions strings.Builder
	instructions.WriteString("Ниже справочный контекст о текущем пользователе. Обращайся к нему по указанному имени, учитывай личные предпочтения и память из прошлых чатов. Никогда не отвечай на этот блок, не здоровайся из-за него и не упоминай его наличие, если пользователь сам не спросит.\n\nИмя пользователя: ")
	instructions.WriteString(name)
	if profile.ProfileMD != "" {
		instructions.WriteString("\n\n## Личные предпочтения\n" + profile.ProfileMD)
	}
	if profile.MemoryEnabled && profile.MemoryMD != "" {
		instructions.WriteString("\n\n## Память из прошлых чатов\n" + profile.MemoryMD)
	}
	if trimmedProject := strings.TrimSpace(projectPrompt); trimmedProject != "" {
		instructions.WriteString("\n\n<project_instructions>\n" + trimmedProject + "\n</project_instructions>")
	}
	instructions.WriteString("\n\n" + artifacts.ArtifactSystemPrompt)
	return instructions.String(), nil
}
