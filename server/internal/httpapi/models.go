package httpapi

import (
	"context"
	"net/http"
	"sync"
	"time"

	"agent-console/server/internal/provider"
)

type modelLister interface {
	ListModels(context.Context) ([]provider.Model, error)
}

type modelCatalog struct {
	mu      sync.Mutex
	models  []provider.Model
	expires time.Time
}

func (a *API) models(w http.ResponseWriter, r *http.Request, _ session) {
	lister, ok := a.live.backend.(modelLister)
	if !ok {
		reply(w, http.StatusOK, []provider.Model{})
		return
	}
	a.catalog.mu.Lock()
	defer a.catalog.mu.Unlock()
	if r.Context().Err() != nil {
		return
	}
	if time.Now().After(a.catalog.expires) {
		models, err := lister.ListModels(r.Context())
		if err != nil {
			a.logger.Warn("load Codex models", "error", err)
			fail(w, http.StatusServiceUnavailable, "models_unavailable", "Codex model catalog is temporarily unavailable.")
			return
		}
		a.catalog.models = models
		a.catalog.expires = time.Now().Add(time.Minute)
	}
	reply(w, http.StatusOK, a.catalog.models)
}
