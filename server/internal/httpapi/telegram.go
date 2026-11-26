package httpapi

import (
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"time"

	"agent-console/server/internal/telegram"
)

type telegramVerifyRequest struct {
	InitData string `json:"initData"`
}

func (a *API) verifyTelegramInitData(w http.ResponseWriter, r *http.Request) {
	if allowed, retryAfter := a.logins.allowWindow("tg:"+a.clientIP(r), 30, time.Minute); !allowed {
		rateLimitedWithRetry(w, retryAfter)
		return
	}
	rawInitData := r.Header.Get("X-Telegram-Init-Data")
	if rawInitData == "" {
		mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if mediaType != "application/json" {
			fail(w, http.StatusUnauthorized, "invalid_telegram_init_data", "Invalid or expired Telegram initData.")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		var req telegramVerifyRequest
		if err := dec.Decode(&req); err != nil {
			fail(w, http.StatusUnauthorized, "invalid_telegram_init_data", "Invalid or expired Telegram initData.")
			return
		}
		var extra any
		if err := dec.Decode(&extra); err != io.EOF {
			fail(w, http.StatusUnauthorized, "invalid_telegram_init_data", "Invalid or expired Telegram initData.")
			return
		}
		rawInitData = req.InitData
	}

	verified, err := telegram.VerifyInitData(rawInitData, a.config.TelegramBotToken, a.config.TelegramInitDataMaxAge, time.Now())
	if err != nil {
		fail(w, http.StatusUnauthorized, "invalid_telegram_init_data", "Invalid or expired Telegram initData.")
		return
	}

	reply(w, http.StatusOK, map[string]any{
		"valid":        true,
		"authDate":     verified.AuthDate.Unix(),
		"queryId":      verified.QueryID,
		"chatType":     verified.ChatType,
		"chatInstance": verified.ChatInstance,
		"user":         verified.User,
	})
}
