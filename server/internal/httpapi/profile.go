package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"agent-console/server/internal/store"
)

func (a *API) getProfile(w http.ResponseWriter, r *http.Request, s session) {
	profile, err := a.db.UserProfile(r.Context(), s.User.ID)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	reply(w, http.StatusOK, profile)
}

func (a *API) patchProfile(w http.ResponseWriter, r *http.Request, s session) {
	var patch struct {
		DisplayName   *string `json:"displayName"`
		AvatarData    *string `json:"avatarData"`
		ProfileMD     *string `json:"profileMd"`
		MemoryMD      *string `json:"memoryMd"`
		MemoryEnabled *bool   `json:"memoryEnabled"`
	}
	// JSON escapes can expand every byte of the text/avatar fields.
	if !decodeLimit(w, r, &patch, 256<<10) {
		return
	}
	profile, err := a.db.UpdateUserProfile(r.Context(), s.User.ID, patch.DisplayName, patch.ProfileMD, patch.MemoryMD, patch.MemoryEnabled, patch.AvatarData)
	switch {
	case errors.Is(err, store.ErrInvalidProfile):
		fail(w, http.StatusBadRequest, "invalid_profile", "Имя должно занимать до 80 символов, аватар — до 128 КБ, а каждый текст — до 4096 байт.")
	case err != nil:
		a.internal(w, r, err)
	default:
		reply(w, http.StatusOK, profile)
	}
}

func (a *API) userInstructions(ctx context.Context, userID string, shared bool) (string, error) {
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
		return "Текущий собеседник: " + name, nil
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
	return instructions.String(), nil
}
