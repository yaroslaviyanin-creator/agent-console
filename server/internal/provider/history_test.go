package provider

import (
	"encoding/json"
	"testing"
)

func TestSplitIndexedTurn(t *testing.T) {
	for _, tc := range []struct {
		name     string
		input    string
		answer   string
		progress string
		duration int64
	}{
		{
			"phased",
			`{"status":"completed","startedAt":100,"completedAt":700,"durationMs":440000,"items":[
				{"type":"agentMessage","phase":"commentary","text":"Проверяю"},
				{"type":"agentMessage","phase":"commentary","text":"::browser-complete{title=\"Служебная отметка\"}"},
				{"type":"agentMessage","phase":"final_answer","text":"Итог"},
				{"type":"agentMessage","phase":"commentary","text":"Поздний статус"}]}`,
			"Итог", "Проверяю\n\nПоздний статус", 440000,
		},
		{
			"legacy completed",
			`{"status":"completed","startedAt":100,"completedAt":104,"items":[
				{"type":"agentMessage","text":"Промежуточное"},
				{"type":"agentMessage","text":"Итог"}]}`,
			"Итог", "Промежуточное", 4000,
		},
		{
			"commentary only",
			`{"status":"completed","items":[{"type":"agentMessage","phase":"commentary","text":"Работаю"}]}`,
			"", "Работаю", -1,
		},
		{
			"interrupted",
			`{"status":"interrupted","durationMs":19000,"items":[{"type":"agentMessage","text":"Проверяю"}]}`,
			"", "Проверяю", -1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var turn indexedTurn
			if err := json.Unmarshal([]byte(tc.input), &turn); err != nil {
				t.Fatal(err)
			}
			got := splitIndexedTurn(turn)
			if got.Answer != tc.answer || got.Progress != tc.progress {
				t.Fatalf("answer %q, progress %q", got.Answer, got.Progress)
			}
			if tc.duration < 0 {
				if got.DurationMs != nil {
					t.Fatalf("unexpected duration %d", *got.DurationMs)
				}
			} else if got.DurationMs == nil || *got.DurationMs != tc.duration {
				t.Fatalf("expected duration %d", tc.duration)
			}
		})
	}
}
