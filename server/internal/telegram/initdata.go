package telegram

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultInitDataMaxAge bounds how long Telegram WebApp initData remains valid for exchange.
	DefaultInitDataMaxAge = time.Hour
	maxFutureAuthSkew     = 5 * time.Minute
)

var ErrInvalidInitData = errors.New("invalid telegram initData")

// User represents the optional verified Telegram user object embedded in initData.
type User struct {
	ID           int64  `json:"id"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name,omitempty"`
	Username     string `json:"username,omitempty"`
	LanguageCode string `json:"language_code,omitempty"`
	IsPremium    bool   `json:"is_premium,omitempty"`
}

// InitData represents verified Telegram Mini App launch parameters.
type InitData struct {
	AuthDate     time.Time `json:"authDate"`
	QueryID      string    `json:"queryId,omitempty"`
	ChatType     string    `json:"chatType,omitempty"`
	ChatInstance string    `json:"chatInstance,omitempty"`
	User         *User     `json:"user,omitempty"`
}

// VerifyInitData validates Telegram Mini App initData using HMAC-SHA256 ("WebAppData")
// across all fields except "hash" (explicitly retaining "signature" for Bot API 8.0+),
// constant-time comparison via hmac.Equal, and strict auth_date freshness checks.
func VerifyInitData(rawInitData, botToken string, maxAge time.Duration, now time.Time) (InitData, error) {
	if strings.TrimSpace(botToken) == "" || strings.TrimSpace(rawInitData) == "" {
		return InitData{}, ErrInvalidInitData
	}
	if maxAge <= 0 {
		maxAge = DefaultInitDataMaxAge
	}
	if now.IsZero() {
		now = time.Now()
	}

	values, err := url.ParseQuery(rawInitData)
	if err != nil || len(values) == 0 {
		return InitData{}, ErrInvalidInitData
	}

	hashes, ok := values["hash"]
	if !ok || len(hashes) != 1 || len(hashes[0]) != 64 {
		return InitData{}, ErrInvalidInitData
	}
	providedHash, err := hex.DecodeString(hashes[0])
	if err != nil || len(providedHash) != sha256.Size {
		return InitData{}, ErrInvalidInitData
	}

	keys := make([]string, 0, len(values))
	for k, vals := range values {
		if k == "hash" {
			continue
		}
		if k == "" || len(vals) != 1 {
			return InitData{}, ErrInvalidInitData
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return InitData{}, ErrInvalidInitData
	}
	slices.Sort(keys)

	pairs := make([]string, len(keys))
	for i, k := range keys {
		pairs[i] = k + "=" + values[k][0]
	}
	dataCheckString := strings.Join(pairs, "\n")

	secretMAC := hmac.New(sha256.New, []byte("WebAppData"))
	secretMAC.Write([]byte(botToken))
	secretKey := secretMAC.Sum(nil)

	checkMAC := hmac.New(sha256.New, secretKey)
	checkMAC.Write([]byte(dataCheckString))
	expectedHash := checkMAC.Sum(nil)

	if !hmac.Equal(providedHash, expectedHash) {
		return InitData{}, ErrInvalidInitData
	}

	authStr := values.Get("auth_date")
	authUnix, err := strconv.ParseInt(authStr, 10, 64)
	if err != nil || authUnix <= 0 {
		return InitData{}, ErrInvalidInitData
	}
	authTime := time.Unix(authUnix, 0)
	if now.Sub(authTime) > maxAge || authTime.Sub(now) > maxFutureAuthSkew {
		return InitData{}, ErrInvalidInitData
	}

	result := InitData{
		AuthDate:     authTime.UTC(),
		QueryID:      values.Get("query_id"),
		ChatType:     values.Get("chat_type"),
		ChatInstance: values.Get("chat_instance"),
	}
	if rawUser := values.Get("user"); rawUser != "" {
		var u User
		if err := json.Unmarshal([]byte(rawUser), &u); err != nil || u.ID <= 0 {
			return InitData{}, ErrInvalidInitData
		}
		result.User = &u
	}
	return result, nil
}
