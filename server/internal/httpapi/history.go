package httpapi

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"time"

	"agent-console/server/internal/provider"
	"agent-console/server/internal/store"
)

type codexHistory interface {
	ListIndexedChats(context.Context) ([]provider.IndexedChat, error)
	ReadIndexedTurns(context.Context, string) ([]provider.IndexedTurn, error)
}

type indexedHistory struct {
	mu      sync.Mutex
	chats   []provider.IndexedChat
	expires time.Time
}

func (a *API) privateHistory(s session) (codexHistory, bool) {
	h, ok := a.live.backend.(codexHistory)
	return h, ok && a.config.CodexHistoryUsername != "" && s.User.Username == a.config.CodexHistoryUsername
}

func (a *API) loadIndex(ctx context.Context, source codexHistory) ([]provider.IndexedChat, error) {
	a.index.mu.Lock()
	defer a.index.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if time.Now().Before(a.index.expires) {
		return a.index.chats, nil
	}
	chats, err := source.ListIndexedChats(ctx)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(chats, func(i, j int) bool { return chats[i].UpdatedAt > chats[j].UpdatedAt })
	a.index.chats = chats
	a.index.expires = time.Now().Add(20 * time.Second)
	return chats, nil
}

func (a *API) indexedChats(w http.ResponseWriter, r *http.Request, s session) {
	source, ok := a.privateHistory(s)
	if !ok {
		fail(w, http.StatusNotFound, "not_found", "Not found.")
		return
	}
	chats, err := a.loadIndex(r.Context(), source)
	if err != nil {
		a.indexError(w, err)
		return
	}
	type item struct {
		store.Chat
		Archived bool `json:"archived"`
	}
	result := make([]item, 0, len(chats))
	for _, c := range chats {
		result = append(result, item{Chat: store.Chat{ID: "codex-" + c.ID, Title: c.Title, Model: c.Model, Provider: "codex", CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}, Archived: c.Archived})
	}
	reply(w, http.StatusOK, result)
}

func (a *API) indexedMessages(w http.ResponseWriter, r *http.Request, s session) {
	source, ok := a.privateHistory(s)
	if !ok {
		fail(w, http.StatusNotFound, "not_found", "Not found.")
		return
	}
	chats, err := a.loadIndex(r.Context(), source)
	if err != nil {
		a.indexError(w, err)
		return
	}
	id := r.PathValue("id")
	for _, chat := range chats {
		if id != "codex-"+chat.ID {
			continue
		}
		turns, err := source.ReadIndexedTurns(r.Context(), chat.ID)
		if err != nil {
			a.indexError(w, err)
			return
		}
		type indexedMessage struct {
			store.Message
			Attachments []provider.IndexedAttachment `json:"attachments,omitempty"`
		}
		messages := []indexedMessage{}
		for _, turn := range turns {
			if turn.Prompt != "" || len(turn.Attachments) > 0 {
				messages = append(messages, indexedMessage{
					Message:     store.Message{ID: turn.ID + "-user", RunID: turn.ID, ChatID: id, Role: "user", Content: turn.Prompt, CreatedAt: turn.CreatedAt},
					Attachments: turn.Attachments,
				})
			}
			if turn.Answer != "" || turn.Progress != "" {
				messages = append(messages, indexedMessage{Message: store.Message{ID: turn.ID, RunID: turn.ID, ChatID: id, Role: "assistant", Content: turn.Answer, Progress: turn.Progress, DurationMs: turn.DurationMs, CreatedAt: turn.CreatedAt}})
			}
		}
		reply(w, http.StatusOK, messages)
		return
	}
	fail(w, http.StatusNotFound, "chat_not_found", "Chat not found.")
}

func (a *API) indexError(w http.ResponseWriter, err error) {
	a.logger.Warn("read Codex index", "error", err)
	fail(w, http.StatusServiceUnavailable, "codex_history_unavailable", "Не удалось прочитать историю Codex.")
}
