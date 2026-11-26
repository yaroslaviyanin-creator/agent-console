package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"
)

type Model struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsDefault        bool     `json:"isDefault"`
	ReasoningEfforts []string `json:"reasoningEfforts,omitempty"`
	DefaultReasoning string   `json:"defaultReasoning,omitempty"`
}

// ListModels asks the installed Codex for the catalog visible to its account.
func (p CodexCliProvider) ListModels(ctx context.Context) ([]Model, error) {
	var models []Model
	err := p.withAppServer(ctx, 7*time.Second, true, func(rpc appRPC) error {
		var err error
		models, err = listModels(rpc)
		return err
	})
	return models, err
}

func readModelCatalog(input io.Writer, output io.Reader) ([]Model, error) {
	rpc, err := initializeAppRPC(input, output)
	if err != nil {
		return nil, err
	}
	return listModels(rpc)
}

func listModels(request appRPC) ([]Model, error) {
	models := []Model{}
	seenModels := make(map[string]bool)
	seenCursors := make(map[string]bool)
	cursor := ""
	for {
		params := map[string]any{"includeHidden": false, "limit": 100}
		if cursor != "" {
			params["cursor"] = cursor
		}
		result, err := request("model/list", params)
		if err != nil {
			return nil, err
		}
		var page struct {
			Data []struct {
				Model       string `json:"model"`
				DisplayName string `json:"displayName"`
				Hidden      bool   `json:"hidden"`
				IsDefault   bool   `json:"isDefault"`
			} `json:"data"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(result, &page); err != nil {
			return nil, err
		}
		if page.Data == nil {
			return nil, errors.New("missing model/list data")
		}
		for _, model := range page.Data {
			if model.Hidden || model.Model == "" || seenModels[model.Model] {
				continue
			}
			name := model.DisplayName
			if name == "" {
				name = model.Model
			}
			models = append(models, Model{ID: model.Model, Name: name, IsDefault: model.IsDefault})
			seenModels[model.Model] = true
		}
		cursor = page.NextCursor
		if cursor == "" {
			return models, nil
		}
		if seenCursors[cursor] {
			return nil, errors.New("repeated Codex model-list cursor")
		}
		seenCursors[cursor] = true
	}
}
