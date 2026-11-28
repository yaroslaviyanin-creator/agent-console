package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
)

type PaidAPIModel struct {
	ID                string  `json:"id"`
	Label             string  `json:"label"`
	InputPricePer1M   float64 `json:"inputPricePer1M"`
	OutputPricePer1M  float64 `json:"outputPricePer1M"`
	SupportsVision    bool    `json:"supportsVision"`
	SupportsReasoning bool    `json:"supportsReasoning"`
}

type PaidAPIConfig struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	BaseURL    string         `json:"baseUrl"`
	APIKeyMask string         `json:"apiKeyMask"`
	HasAPIKey  bool           `json:"hasApiKey"`
	Models     []PaidAPIModel `json:"models"`
	Enabled    bool           `json:"enabled"`
	CreatedAt  int64          `json:"createdAt"`
	APIKeyEnc  string         `json:"-"`
}

func deriveAPIKeyCipher(secret string) (cipher.AEAD, error) {
	if secret == "" {
		secret = "agent-console-dev-local-api-keys-secret"
	}
	sum := sha256.Sum256([]byte("paid_api_key_v1:" + secret))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func EncryptAPIKey(plaintext, secret string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	aead, err := deriveAPIKeyCipher(secret)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return "v1:" + base64.StdEncoding.EncodeToString(sealed), nil
}

func DecryptAPIKey(encoded, secret string) (string, error) {
	if encoded == "" {
		return "", nil
	}
	rawB64, ok := strings.CutPrefix(encoded, "v1:")
	if !ok {
		return "", errors.New("unsupported encrypted api key format")
	}
	raw, err := base64.StdEncoding.DecodeString(rawB64)
	if err != nil {
		return "", err
	}
	aead, err := deriveAPIKeyCipher(secret)
	if err != nil {
		return "", err
	}
	if len(raw) < aead.NonceSize() {
		return "", errors.New("corrupted encrypted api key")
	}
	nonce, ciphertext := raw[:aead.NonceSize()], raw[aead.NonceSize():]
	plain, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func MaskAPIKey(plaintext string) string {
	trimmed := strings.TrimSpace(plaintext)
	if trimmed == "" {
		return ""
	}
	if len(trimmed) <= 6 {
		return "••••"
	}
	prefix := ""
	if strings.HasPrefix(trimmed, "sk-") {
		prefix = "sk-"
	}
	return prefix + "••••" + trimmed[len(trimmed)-4:]
}

func scanPaidAPIConfig(row interface{ Scan(...any) error }, secret string) (PaidAPIConfig, error) {
	var c PaidAPIConfig
	var modelsRaw string
	err := row.Scan(&c.ID, &c.Name, &c.BaseURL, &c.APIKeyEnc, &modelsRaw, &c.Enabled, &c.CreatedAt)
	if err != nil {
		return PaidAPIConfig{}, err
	}
	c.Models = []PaidAPIModel{}
	if strings.TrimSpace(modelsRaw) != "" {
		_ = json.Unmarshal([]byte(modelsRaw), &c.Models)
		if c.Models == nil {
			c.Models = []PaidAPIModel{}
		}
	}
	c.HasAPIKey = c.APIKeyEnc != ""
	if c.HasAPIKey {
		if plain, decErr := DecryptAPIKey(c.APIKeyEnc, secret); decErr == nil {
			c.APIKeyMask = MaskAPIKey(plain)
		} else {
			c.APIKeyMask = "••••"
		}
	}
	return c, nil
}

func (s *Store) ListPaidAPIConfigs(ctx context.Context, secret string) ([]PaidAPIConfig, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, base_url, api_key_enc, models_json, enabled, created_at FROM paid_api_configs ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PaidAPIConfig{}
	for rows.Next() {
		c, err := scanPaidAPIConfig(rows, secret)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) GetPaidAPIConfig(ctx context.Context, id, secret string) (PaidAPIConfig, error) {
	return scanPaidAPIConfig(
		s.db.QueryRowContext(ctx, `SELECT id, name, base_url, api_key_enc, models_json, enabled, created_at FROM paid_api_configs WHERE id=?`, id),
		secret,
	)
}

func (s *Store) CreatePaidAPIConfig(ctx context.Context, name, baseURL, apiKeyPlain string, models []PaidAPIModel, enabled bool, secret string) (PaidAPIConfig, error) {
	enc, err := EncryptAPIKey(apiKeyPlain, secret)
	if err != nil {
		return PaidAPIConfig{}, err
	}
	if models == nil {
		models = []PaidAPIModel{}
	}
	modelsJSON, err := json.Marshal(models)
	if err != nil {
		return PaidAPIConfig{}, err
	}
	id := randomHex(16)
	now := time.Now().UnixMilli()
	_, err = s.db.ExecContext(ctx, `INSERT INTO paid_api_configs (id, name, base_url, api_key_enc, models_json, enabled, created_at) VALUES (?,?,?,?,?,?,?)`,
		id, name, baseURL, enc, string(modelsJSON), enabled, now)
	if err != nil {
		return PaidAPIConfig{}, err
	}
	return PaidAPIConfig{
		ID:         id,
		Name:       name,
		BaseURL:    baseURL,
		APIKeyMask: MaskAPIKey(apiKeyPlain),
		HasAPIKey:  enc != "",
		Models:     models,
		Enabled:    enabled,
		CreatedAt:  now,
		APIKeyEnc:  enc,
	}, nil
}

func (s *Store) UpdatePaidAPIConfig(ctx context.Context, id string, name, baseURL, apiKeyPlain *string, models *[]PaidAPIModel, enabled *bool, secret string) (PaidAPIConfig, error) {
	existing, err := s.GetPaidAPIConfig(ctx, id, secret)
	if err != nil {
		return PaidAPIConfig{}, err
	}
	if name != nil {
		existing.Name = *name
	}
	if baseURL != nil {
		existing.BaseURL = *baseURL
	}
	if apiKeyPlain != nil && strings.TrimSpace(*apiKeyPlain) != "" {
		enc, err := EncryptAPIKey(strings.TrimSpace(*apiKeyPlain), secret)
		if err != nil {
			return PaidAPIConfig{}, err
		}
		existing.APIKeyEnc = enc
		existing.HasAPIKey = true
		existing.APIKeyMask = MaskAPIKey(*apiKeyPlain)
	}
	if models != nil {
		existing.Models = *models
		if existing.Models == nil {
			existing.Models = []PaidAPIModel{}
		}
	}
	if enabled != nil {
		existing.Enabled = *enabled
	}
	modelsJSON, err := json.Marshal(existing.Models)
	if err != nil {
		return PaidAPIConfig{}, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE paid_api_configs SET name=?, base_url=?, api_key_enc=?, models_json=?, enabled=? WHERE id=?`,
		existing.Name, existing.BaseURL, existing.APIKeyEnc, string(modelsJSON), existing.Enabled, id)
	if err != nil {
		return PaidAPIConfig{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return PaidAPIConfig{}, sql.ErrNoRows
	}
	return existing, nil
}

func (s *Store) DeletePaidAPIConfig(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM paid_api_configs WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ResolvePaidAPIModel finds the enabled config and model definition for the requested modelID.
func (s *Store) ResolvePaidAPIModel(ctx context.Context, modelID, secret string) (PaidAPIConfig, PaidAPIModel, string, error) {
	configs, err := s.ListPaidAPIConfigs(ctx, secret)
	if err != nil {
		return PaidAPIConfig{}, PaidAPIModel{}, "", err
	}
	for _, cfg := range configs {
		if !cfg.Enabled || cfg.APIKeyEnc == "" {
			continue
		}
		for _, m := range cfg.Models {
			if m.ID == modelID || modelID == "" || modelID == "default" || modelID == "paid_api" {
				key, err := DecryptAPIKey(cfg.APIKeyEnc, secret)
				if err != nil {
					return PaidAPIConfig{}, PaidAPIModel{}, "", err
				}
				return cfg, m, key, nil
			}
		}
	}
	return PaidAPIConfig{}, PaidAPIModel{}, "", sql.ErrNoRows
}

// HasEnabledPaidAPI reports whether at least one enabled paid_api config has a key and at least one model.
func (s *Store) HasEnabledPaidAPI(ctx context.Context) bool {
	rows, err := s.db.QueryContext(ctx, `SELECT models_json FROM paid_api_configs WHERE enabled=1 AND api_key_enc!=''`)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			continue
		}
		var models []PaidAPIModel
		if json.Unmarshal([]byte(raw), &models) == nil && len(models) > 0 {
			return true
		}
	}
	return false
}
