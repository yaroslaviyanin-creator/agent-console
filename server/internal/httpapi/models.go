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

func (a *API) models(w http.ResponseWriter, r *http.Request, _ session) {
	provID := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("provider")))
	if provID == "" {
		provID = a.defaultProvider
	}
	prov, ok := a.live.provider(provID)
	if !ok {
		fail(w, http.StatusUnprocessableEntity, "provider_unavailable", "Requested provider is not available.")
		return
	}
	lister, ok := prov.(modelLister)
	if !ok {
		reply(w, http.StatusOK, []provider.Model{})
		return
	}
	entry := a.catalog.entry(provID)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if r.Context().Err() != nil {
		return
	}
	if time.Now().After(entry.expires) {
		models, err := lister.ListModels(r.Context())
		if err != nil {
			a.logger.Warn("load provider models", "provider", provID, "error", err)
			fail(w, http.StatusServiceUnavailable, "models_unavailable", "Model catalog is temporarily unavailable.")
			return
		}
		if models == nil {
			models = []provider.Model{}
		}
		entry.models = models
		entry.expires = time.Now().Add(time.Minute)
	}
	reply(w, http.StatusOK, entry.models)
}
