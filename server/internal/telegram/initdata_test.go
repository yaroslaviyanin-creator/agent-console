package telegram

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func signInitData(fields map[string]string, botToken string) string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	pairs := make([]string, len(keys))
	for i, k := range keys {
		pairs[i] = k + "=" + fields[k]
	}
	dataCheck := strings.Join(pairs, "\n")

	secretMAC := hmac.New(sha256.New, []byte("WebAppData"))
	secretMAC.Write([]byte(botToken))
	checkMAC := hmac.New(sha256.New, secretMAC.Sum(nil))
	checkMAC.Write([]byte(dataCheck))
	sig := hex.EncodeToString(checkMAC.Sum(nil))

	q := url.Values{}
	for k, v := range fields {
		q.Set(k, v)
	}
	q.Set("hash", sig)
	return q.Encode()
}

func TestVerifyInitData(t *testing.T) {
	const token = "123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11"
	now := time.Unix(1_795_700_000, 0)

	// Valid initData WITH Bot API 8.0+ signature field must succeed.
	validRaw := signInitData(map[string]string{
		"auth_date": strconv.FormatInt(now.Add(-5*time.Minute).Unix(), 10),
		"query_id":  "AAHdF6IQAAAAAN0XohDhrOrc",
		"signature": "ed25519-telegram-bot-api-8-signature-value",
		"user":      `{"id":424242,"first_name":"Yaroslav","username":"yaroslav"}`,
	}, token)

	parsed, err := VerifyInitData(validRaw, token, time.Hour, now)
	if err != nil {
		t.Fatalf("expected valid initData with signature to pass, got %v", err)
	}
	if parsed.User == nil || parsed.User.ID != 424242 || parsed.User.Username != "yaroslav" {
		t.Fatalf("unexpected parsed user: %+v", parsed.User)
	}

	// Forged hash must fail.
	forged := strings.Replace(validRaw, "hash=", "hash=0000", 1)
	if _, err := VerifyInitData(forged, token, time.Hour, now); err == nil {
		t.Fatal("expected forged hash to fail")
	}

	// Tampered field after signing must fail.
	tampered := strings.Replace(validRaw, "424242", "999999", 1)
	if _, err := VerifyInitData(tampered, token, time.Hour, now); err == nil {
		t.Fatal("expected tampered user id to fail")
	}

	// Expired auth_date (> 1h) must fail.
	expiredRaw := signInitData(map[string]string{
		"auth_date": strconv.FormatInt(now.Add(-61*time.Minute).Unix(), 10),
		"user":      `{"id":424242,"first_name":"Yaroslav"}`,
	}, token)
	if _, err := VerifyInitData(expiredRaw, token, time.Hour, now); err == nil {
		t.Fatal("expected expired initData to fail")
	}

	// Far-future auth_date (> +5m) must fail.
	futureRaw := signInitData(map[string]string{
		"auth_date": strconv.FormatInt(now.Add(6*time.Minute).Unix(), 10),
		"user":      `{"id":424242,"first_name":"Yaroslav"}`,
	}, token)
	if _, err := VerifyInitData(futureRaw, token, time.Hour, now); err == nil {
		t.Fatal("expected future initData to fail")
	}

	// Missing bot token must fail closed.
	if _, err := VerifyInitData(validRaw, "", time.Hour, now); err == nil {
		t.Fatal("expected empty bot token to fail closed")
	}
}
