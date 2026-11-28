package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"agent-console/server/internal/provider"
	"agent-console/server/internal/store"
	"agent-console/server/internal/webfetch"
)

type paidAPIResolver struct {
	db     *store.Store
	secret string
}

func validatePaidAPIBaseURL(raw string) (string, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(trimmed)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", errors.New("baseUrl должен начинаться с https://")
	}
	if err := webfetch.ValidateTargetURL(u); err != nil {
		return "", errors.New("baseUrl указывает на запрещённый или внутренний адрес")
	}
	return trimmed, nil
}

func (r *paidAPIResolver) ResolveModel(ctx context.Context, modelID string) (provider.OpenAICompatEndpoint, error) {
	if r.db == nil {
		return provider.OpenAICompatEndpoint{}, errors.New("database is not configured")
	}
	cfg, m, key, err := r.db.ResolvePaidAPIModel(ctx, modelID, r.secret)
	if err != nil {
		return provider.OpenAICompatEndpoint{}, err
	}
	cleanURL, err := validatePaidAPIBaseURL(cfg.BaseURL)
	if err != nil {
		return provider.OpenAICompatEndpoint{}, err
	}
	return provider.OpenAICompatEndpoint{
		BaseURL: cleanURL,
		APIKey:  key,
		Model: provider.OpenAICompatModelSpec{
			ID:                m.ID,
			Label:             m.Label,
			SupportsVision:    m.SupportsVision,
			SupportsReasoning: m.SupportsReasoning,
		},
	}, nil
}

func (r *paidAPIResolver) ListModels(ctx context.Context) ([]provider.Model, error) {
	if r.db == nil {
		return []provider.Model{}, nil
	}
	configs, err := r.db.ListPaidAPIConfigs(ctx, r.secret)
	if err != nil {
		return nil, err
	}
	models := make([]provider.Model, 0)
	first := true
	for _, cfg := range configs {
		if !cfg.Enabled || !cfg.HasAPIKey {
			continue
		}
		for _, m := range cfg.Models {
			if strings.TrimSpace(m.ID) == "" {
				continue
			}
			label := strings.TrimSpace(m.Label)
			if label == "" {
				label = m.ID
			}
			if cfg.Name != "" {
				label = cfg.Name + ": " + label
			}
			models = append(models, provider.Model{
				ID:        m.ID,
				Name:      label,
				IsDefault: first,
			})
			first = false
		}
	}
	return models, nil
}

func normalizePaidAPIModels(in []store.PaidAPIModel) ([]store.PaidAPIModel, error) {
	if len(in) > 100 {
		return nil, errors.New("слишком много моделей (максимум 100)")
	}
	out := make([]store.PaidAPIModel, 0, len(in))
	for _, m := range in {
		id := strings.TrimSpace(m.ID)
		if id == "" || len(id) > 128 {
			return nil, errors.New("некорректный ID модели")
		}
		label := strings.TrimSpace(m.Label)
		if label == "" {
			label = id
		}
		if m.InputPricePer1M < 0 || m.OutputPricePer1M < 0 {
			return nil, errors.New("цена модели не может быть отрицательной")
		}
		out = append(out, store.PaidAPIModel{
			ID:                id,
			Label:             label,
			InputPricePer1M:   m.InputPricePer1M,
			OutputPricePer1M:  m.OutputPricePer1M,
			SupportsVision:    m.SupportsVision,
			SupportsReasoning: m.SupportsReasoning,
		})
	}
	return out, nil
}

func (a *API) adminListAPIProviders(w http.ResponseWriter, r *http.Request, _ session) {
	items, err := a.db.ListPaidAPIConfigs(r.Context(), a.config.APIKeysSecret)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	reply(w, http.StatusOK, items)
}

func (a *API) adminCreateAPIProvider(w http.ResponseWriter, r *http.Request, _ session) {
	var req struct {
		Name    string               `json:"name"`
		BaseURL string               `json:"baseUrl"`
		APIKey  string               `json:"apiKey"`
		Models  []store.PaidAPIModel `json:"models"`
		Enabled *bool                `json:"enabled"`
	}
	if !decodeLimit(w, r, &req, 64<<10) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 80 {
		fail(w, http.StatusBadRequest, "invalid_api_provider", "Название провайдера должно быть от 1 до 80 символов.")
		return
	}
	baseURL, err := validatePaidAPIBaseURL(req.BaseURL)
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid_api_provider", err.Error())
		return
	}
	apiKey := strings.TrimSpace(req.APIKey)
	if apiKey == "" || len(apiKey) > 512 {
		fail(w, http.StatusBadRequest, "invalid_api_provider", "Укажите корректный API-ключ.")
		return
	}
	models, err := normalizePaidAPIModels(req.Models)
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid_api_provider", err.Error())
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	created, err := a.db.CreatePaidAPIConfig(r.Context(), name, baseURL, apiKey, models, enabled, a.config.APIKeysSecret)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	a.catalog.invalidate("paid_api")
	reply(w, http.StatusCreated, created)
}

func (a *API) adminUpdateAPIProvider(w http.ResponseWriter, r *http.Request, _ session) {
	var req struct {
		Name    *string               `json:"name"`
		BaseURL *string               `json:"baseUrl"`
		APIKey  *string               `json:"apiKey"`
		Models  *[]store.PaidAPIModel `json:"models"`
		Enabled *bool                 `json:"enabled"`
	}
	if !decodeLimit(w, r, &req, 64<<10) {
		return
	}
	if req.Name != nil {
		trimmed := strings.TrimSpace(*req.Name)
		if trimmed == "" || len(trimmed) > 80 {
			fail(w, http.StatusBadRequest, "invalid_api_provider", "Название провайдера должно быть от 1 до 80 символов.")
			return
		}
		req.Name = &trimmed
	}
	if req.BaseURL != nil {
		cleanURL, err := validatePaidAPIBaseURL(*req.BaseURL)
		if err != nil {
			fail(w, http.StatusBadRequest, "invalid_api_provider", err.Error())
			return
		}
		req.BaseURL = &cleanURL
	}
	if req.Models != nil {
		normalized, err := normalizePaidAPIModels(*req.Models)
		if err != nil {
			fail(w, http.StatusBadRequest, "invalid_api_provider", err.Error())
			return
		}
		req.Models = &normalized
	}
	updated, err := a.db.UpdatePaidAPIConfig(r.Context(), r.PathValue("id"), req.Name, req.BaseURL, req.APIKey, req.Models, req.Enabled, a.config.APIKeysSecret)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		fail(w, http.StatusNotFound, "api_provider_not_found", "API provider not found.")
	case err != nil:
		a.internal(w, r, err)
	default:
		a.catalog.invalidate("paid_api")
		reply(w, http.StatusOK, updated)
	}
}

func (a *API) adminDeleteAPIProvider(w http.ResponseWriter, r *http.Request, _ session) {
	err := a.db.DeletePaidAPIConfig(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		fail(w, http.StatusNotFound, "api_provider_not_found", "API provider not found.")
	case err != nil:
		a.internal(w, r, err)
	default:
		a.catalog.invalidate("paid_api")
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *API) adminTestAPIProvider(w http.ResponseWriter, r *http.Request, _ session) {
	cfg, err := a.db.GetPaidAPIConfig(r.Context(), r.PathValue("id"), a.config.APIKeysSecret)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		fail(w, http.StatusNotFound, "api_provider_not_found", "API provider not found.")
		return
	case err != nil:
		a.internal(w, r, err)
		return
	}
	if _, err := validatePaidAPIBaseURL(cfg.BaseURL); err != nil {
		fail(w, http.StatusBadRequest, "invalid_api_provider", err.Error())
		return
	}
	key, err := store.DecryptAPIKey(cfg.APIKeyEnc, a.config.APIKeysSecret)
	if err != nil {
		a.internal(w, r, err)
		return
	}
	if err := provider.TestOpenAICompatConnection(r.Context(), nil, cfg.BaseURL, key); err != nil {
		fail(w, http.StatusBadGateway, "api_provider_test_failed", "Проверка соединения не удалась: "+err.Error())
		return
	}
	reply(w, http.StatusOK, map[string]any{"ok": true})
}
