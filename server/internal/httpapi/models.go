package httpapi

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"agent-console/server/internal/provider"
)

type modelLister interface {
	ListModels(context.Context) ([]provider.Model, error)
}

type modelCacheEntry struct {
	mu      sync.Mutex
	models  []provider.Model
	expires time.Time
}

type modelCatalog struct {
	mu      sync.Mutex
	entries map[string]*modelCacheEntry
}

func (c *modelCatalog) entry(providerID string) *modelCacheEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]*modelCacheEntry)
	}
	e, ok := c.entries[providerID]
	if !ok {
		e = &modelCacheEntry{}
		c.entries[providerID] = e
	}
	return e
}

func (c *modelCatalog) invalidate(providerID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, providerID)
}

func (a *API) cachedProviderModels(ctx context.Context, provID string) ([]provider.Model, error) {
	prov, ok := a.live.provider(provID)
	if !ok {
		return nil, nil
	}
	lister, ok := prov.(modelLister)
	if !ok {
		return []provider.Model{}, nil
	}
	entry := a.catalog.entry(provID)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if time.Now().After(entry.expires) {
		models, err := lister.ListModels(ctx)
		if err != nil {
			return nil, err
		}
		if models == nil {
			models = []provider.Model{}
		}
		entry.models = models
		entry.expires = time.Now().Add(time.Minute)
	}
	return entry.models, nil
}

// resolveDefaultModelID returns the real default model ID for the given provider,
// falling back to known provider defaults if the catalog is unavailable.
func (a *API) resolveDefaultModelID(ctx context.Context, provID string) string {
	provID = strings.ToLower(strings.TrimSpace(provID))
	lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if models, err := a.cachedProviderModels(lookupCtx, provID); err == nil && len(models) > 0 {
		for _, m := range models {
			if m.IsDefault && m.ID != "" {
				return m.ID
			}
		}
		if models[0].ID != "" {
			return models[0].ID
		}
	}
	switch provID {
	case "claude":
		return "sonnet"
	case "antigravity":
		return "gemini-3.8-flash-high"
	case "stub":
		return "stub"
	default:
		return ""
	}
}

func isDefaultModelPlaceholder(modelID, provID string) bool {
	trimmed := strings.TrimSpace(modelID)
	return trimmed == "" || trimmed == "default" || strings.EqualFold(trimmed, provID)
}

func (a *API) models(w http.ResponseWriter, r *http.Request, _ session) {
	provID := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("provider")))
	if provID == "" {
		provID = a.defaultProvider
	}
	if _, ok := a.live.provider(provID); !ok {
		fail(w, http.StatusUnprocessableEntity, "provider_unavailable", "Requested provider is not available.")
		return
	}
	models, err := a.cachedProviderModels(r.Context(), provID)
	if err != nil {
		a.logger.Warn("load provider models", "provider", provID, "error", err)
		fail(w, http.StatusServiceUnavailable, "models_unavailable", "Model catalog is temporarily unavailable.")
		return
	}
	reply(w, http.StatusOK, models)
}
