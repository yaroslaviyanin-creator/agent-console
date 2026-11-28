package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"agent-console/server/internal/config"
	"agent-console/server/internal/provider"
	"agent-console/server/internal/store"
)

type scriptedMemoryProvider struct {
	reply string
	err   error
}

func (p scriptedMemoryProvider) Stream(_ context.Context, _ provider.Request) (<-chan provider.Event, error) {
	ch := make(chan provider.Event, 2)
	if p.err != nil {
		ch <- provider.Event{Type: provider.Error, Err: p.err}
		close(ch)
		return ch, nil
	}
	if p.reply != "" {
		ch <- provider.Event{Type: provider.Text, Delta: p.reply}
	}
	ch <- provider.Event{Type: provider.Done}
	close(ch)
	return ch, nil
}

func TestParseMemoryFacts(t *testing.T) {
	facts, ok := parseMemoryFacts(`["Имя: Ярослав", "Стек: Go, React"]`)
	if !ok || len(facts) != 2 || facts[0] != "Имя: Ярослав" || facts[1] != "Стек: Go, React" {
		t.Fatalf("clean json parse failed: ok=%v facts=%v", ok, facts)
	}

	facts, ok = parseMemoryFacts("```json\n[\"Город: Алматы\"]\n```")
	if !ok || len(facts) != 1 || facts[0] != "Город: Алматы" {
		t.Fatalf("fenced json parse failed: ok=%v facts=%v", ok, facts)
	}

	facts, ok = parseMemoryFacts("Вот найденные факты:\n[\"Проект: Agent Console\"]\nКонец.")
	if !ok || len(facts) != 1 || facts[0] != "Проект: Agent Console" {
		t.Fatalf("prose-wrapped json parse failed: ok=%v facts=%v", ok, facts)
	}

	facts, ok = parseMemoryFacts("[]")
	if !ok || len(facts) != 0 {
		t.Fatalf("empty array parse failed: ok=%v facts=%v", ok, facts)
	}

	if _, ok := parseMemoryFacts("Не удалось определить факты"); ok {
		t.Fatal("expected unparseable prose without JSON/bullets to return ok=false")
	}
}

func TestMergeMemoryFacts(t *testing.T) {
	merged := mergeMemoryFacts("- Имя: Ярослав\n- Стек: React", []string{"Стек: React", "Город: Алматы"})
	want := "- Имя: Ярослав\n- Стек: React\n- Город: Алматы"
	if merged != want {
		t.Fatalf("dedup merge mismatch: got %q want %q", merged, want)
	}

	updated := mergeMemoryFacts("- Имя: Ярослав\n- Город: Алматы", []string{"Имя: Алекс"})
	wantUpdated := "- Имя: Алекс\n- Город: Алматы"
	if updated != wantUpdated {
		t.Fatalf("key update merge mismatch: got %q want %q", updated, wantUpdated)
	}

	fromLegacy := mergeMemoryFacts("[]", []string{"Цель: Запуск проекта"})
	if fromLegacy != "- Цель: Запуск проекта" {
		t.Fatalf("legacy [] cleanup mismatch: got %q", fromLegacy)
	}
}

func TestMemoryWorkerPreservesMemoryOnEmptyOrErrorAndMergesFenced(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	owner, err := db.CreateUserWithRole(ctx, "owner", []byte("h"), "user")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveUserMemory(ctx, owner.ID, "- Имя: Ярослав", 10); err != nil {
		t.Fatal(err)
	}
	chat, err := db.CreateChat(ctx, owner.ID, "Личный", "codex", "default")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.BeginRun(ctx, owner.ID, chat.ID, "r1", "Я живу в Алматы", "codex", "default"); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishRun(ctx, owner.ID, chat.ID, "r1", "Отлично!", "", "", 100, nil); err != nil {
		t.Fatal(err)
	}

	runWith := func(p provider.Provider) error {
		api := &API{
			db:             db,
			config:         config.Config{MemoryMinNewMessages: 1},
			logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
			live:           newLiveRuns(ctx, p, 1),
			memoryProvider: "stub",
		}
		w := memoryWorker{updating: make(map[string]bool)}
		return w.update(ctx, api, owner.ID)
	}

	// 1. Empty [] must NOT overwrite memory_md or advance memory_updated_at.
	if err := runWith(scriptedMemoryProvider{reply: "[]"}); err != nil {
		t.Fatalf("empty reply update failed: %v", err)
	}
	prof, err := db.UserProfile(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prof.MemoryMD != "- Имя: Ярослав" || prof.MemoryUpdatedAt != 10 {
		t.Fatalf("empty [] modified memory: md=%q updatedAt=%d", prof.MemoryMD, prof.MemoryUpdatedAt)
	}

	// 2. Provider error must NOT overwrite memory_md.
	if err := runWith(scriptedMemoryProvider{err: errors.New("boom")}); err == nil {
		t.Fatal("expected error from failing provider")
	}
	prof, _ = db.UserProfile(ctx, owner.ID)
	if prof.MemoryMD != "- Имя: Ярослав" || prof.MemoryUpdatedAt != 10 {
		t.Fatalf("error modified memory: md=%q updatedAt=%d", prof.MemoryMD, prof.MemoryUpdatedAt)
	}

	// 3. Fenced JSON facts merge cleanly with existing memory.
	if err := runWith(scriptedMemoryProvider{reply: "```json\n[\"Город: Алматы\"]\n```"}); err != nil {
		t.Fatalf("valid update failed: %v", err)
	}
	prof, _ = db.UserProfile(ctx, owner.ID)
	if prof.MemoryMD != "- Имя: Ярослав\n- Город: Алматы" || prof.MemoryUpdatedAt <= 10 {
		t.Fatalf("unexpected merged memory: md=%q updatedAt=%d", prof.MemoryMD, prof.MemoryUpdatedAt)
	}
}

func TestMemoryWorkerSkipsSharedChats(t *testing.T) {
	ctx := t.Context()
	db, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	owner, err := db.CreateUserWithRole(ctx, "owner", []byte("h"), "user")
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUserWithRole(ctx, "other", []byte("h"), "user")
	if err != nil {
		t.Fatal(err)
	}
	sharedChat, err := db.CreateChat(ctx, owner.ID, "Общий", "codex", "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetChatShare(ctx, owner.ID, sharedChat.ID, other.ID, "write"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BeginRun(ctx, owner.ID, sharedChat.ID, "r1", "Меня зовут Секрет", "codex", "default"); err != nil {
		t.Fatal(err)
	}
	if err := db.FinishRun(ctx, owner.ID, sharedChat.ID, "r1", "Принято", "", "", 50, nil); err != nil {
		t.Fatal(err)
	}

	pending, err := db.UsersPendingMemory(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected shared chat messages to be excluded from pending memory, got %v", pending)
	}

	// Even if update() is called directly, no personal messages exist so memory stays empty.
	api := &API{
		db:             db,
		config:         config.Config{MemoryMinNewMessages: 1, MemoryCheckInterval: time.Second},
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		live:           newLiveRuns(ctx, scriptedMemoryProvider{reply: `["Имя: Секрет"]`}, 1),
		memoryProvider: "stub",
	}
	w := memoryWorker{updating: make(map[string]bool)}
	if err := w.update(ctx, api, owner.ID); err != nil {
		t.Fatal(err)
	}
	prof, _ := db.UserProfile(ctx, owner.ID)
	if prof.MemoryMD != "" {
		t.Fatalf("expected shared chat not to populate memory, got %q", prof.MemoryMD)
	}
}
