package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agent-console/server/internal/config"
	"agent-console/server/internal/provider"
	"agent-console/server/internal/store"
)

type fixtureHistory struct{ provider.StubProvider }

func (fixtureHistory) ListIndexedChats(context.Context) ([]provider.IndexedChat, error) {
	return []provider.IndexedChat{{ID: "existing", Title: "Private title", Archived: true}}, nil
}

func (fixtureHistory) ReadIndexedTurns(context.Context, string) ([]provider.IndexedTurn, error) {
	return []provider.IndexedTurn{{ID: "turn", Prompt: "Private prompt", Answer: "Private answer", Progress: "Intermediate step"}}, nil
}

func TestCodexIndexOwnerOnly(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tokenFor := func(name string) string {
		t.Helper()
		if err := db.CreateUser(ctx, name, []byte("fixture")); err != nil {
			t.Fatal(err)
		}
		u, _, err := db.Credentials(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		_, token, err := db.IssueDevice(ctx, u.ID, "fixture")
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	owner, other := tokenFor("owner"), tokenFor("other")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	api := newHandler(ctx, db, config.Config{Provider: "codex", CodexHistoryUsername: "owner"}, logger, fixtureHistory{})
	for _, tc := range []struct {
		path, token, text string
		status            int
	}{
		{"/api/codex/chats", "", "", 401},
		{"/api/codex/chats", other, "", 404},
		{"/api/codex/chats/codex-existing/messages", other, "", 404},
		{"/api/codex/chats/codex-missing/messages", owner, "", 404},
		{"/api/codex/chats", owner, `"archived":true`, 200},
		{"/api/codex/chats/codex-existing/messages", owner, `"progress":"Intermediate step"`, 200},
	} {
		r := httptest.NewRequest(http.MethodGet, tc.path, nil)
		if tc.token != "" {
			r.Header.Set("Authorization", "Bearer "+tc.token)
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != tc.status || (tc.text != "" && !strings.Contains(w.Body.String(), tc.text)) ||
			(tc.status != 200 && strings.Contains(w.Body.String(), "Private")) {
			t.Fatalf("GET %s: %d %s", tc.path, w.Code, w.Body)
		}
	}
}
