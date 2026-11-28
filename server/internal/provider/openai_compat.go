package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type OpenAICompatModelSpec struct {
	ID                string
	Label             string
	SupportsVision    bool
	SupportsReasoning bool
}

type OpenAICompatEndpoint struct {
	BaseURL string
	APIKey  string
	Model   OpenAICompatModelSpec
}

type OpenAICompatResolver interface {
	ResolveModel(ctx context.Context, modelID string) (OpenAICompatEndpoint, error)
	ListModels(ctx context.Context) ([]Model, error)
}

type OpenAICompatProvider struct {
	Resolver   OpenAICompatResolver
	HTTPClient *http.Client
}

func NewOpenAICompatProvider(resolver OpenAICompatResolver) *OpenAICompatProvider {
	return &OpenAICompatProvider{
		Resolver:   resolver,
		HTTPClient: &http.Client{Timeout: 5 * time.Minute},
	}
}

func (p *OpenAICompatProvider) ListModels(ctx context.Context) ([]Model, error) {
	if p.Resolver == nil {
		return []Model{}, nil
	}
	return p.Resolver.ListModels(ctx)
}

func (p *OpenAICompatProvider) client() *http.Client {
	if p.HTTPClient != nil {
		return p.HTTPClient
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

func (p *OpenAICompatProvider) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	if p.Resolver == nil {
		return nil, errors.New("paid_api resolver is not configured")
	}
	ep, err := p.Resolver.ResolveModel(ctx, req.Model)
	if err != nil {
		return nil, err
	}
	endpointURL := strings.TrimRight(ep.BaseURL, "/") + "/chat/completions"

	type message struct {
		Role    string `json:"role"`
		Content any    `json:"content"`
	}
	msgs := make([]message, 0, len(req.History)+2)
	if strings.TrimSpace(req.SystemInstructions) != "" {
		msgs = append(msgs, message{Role: "system", Content: req.SystemInstructions})
	}
	for _, h := range req.History {
		if h.Role != "user" && h.Role != "assistant" {
			continue
		}
		if strings.TrimSpace(h.Content) == "" {
			continue
		}
		msgs = append(msgs, message{Role: h.Role, Content: h.Content})
	}
	if len(req.Images) > 0 && ep.Model.SupportsVision {
		parts := make([]map[string]any, 0, len(req.Images)+1)
		if req.Prompt != "" {
			parts = append(parts, map[string]any{"type": "text", "text": req.Prompt})
		}
		for _, img := range req.Images {
			b64 := base64.StdEncoding.EncodeToString(img.Data)
			parts = append(parts, map[string]any{
				"type": "image_url",
				"image_url": map[string]string{
					"url": "data:" + img.MIMEType + ";base64," + b64,
				},
			})
		}
		msgs = append(msgs, message{Role: "user", Content: parts})
	} else {
		msgs = append(msgs, message{Role: "user", Content: req.Prompt})
	}

	payload := map[string]any{
		"model":    ep.Model.ID,
		"messages": msgs,
		"stream":   true,
		"stream_options": map[string]bool{
			"include_usage": true,
		},
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	if ep.APIKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+ep.APIKey)
	}

	resp, err := p.client().Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request paid_api: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("paid_api returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}

	out := make(chan Event)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		var usage *Usage
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 32<<10), 2<<20)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, ":") {
				continue
			}
			data, ok := strings.CutPrefix(line, "data:")
			if !ok {
				continue
			}
			data = strings.TrimSpace(data)
			if data == "[DONE]" {
				break
			}
			var chunk struct {
				Choices []struct {
					Delta struct {
						Content          string `json:"content"`
						ReasoningContent string `json:"reasoning_content"`
						Reasoning        string `json:"reasoning"`
					} `json:"delta"`
				} `json:"choices"`
				Usage *struct {
					PromptTokens        int64 `json:"prompt_tokens"`
					CompletionTokens    int64 `json:"completion_tokens"`
					PromptTokensDetails *struct {
						CachedTokens int64 `json:"cached_tokens"`
					} `json:"prompt_tokens_details"`
					CompletionTokensDetails *struct {
						ReasoningTokens int64 `json:"reasoning_tokens"`
					} `json:"completion_tokens_details"`
				} `json:"usage"`
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}
			if chunk.Error != nil && chunk.Error.Message != "" {
				send(ctx, out, Event{Type: Error, Err: errors.New(chunk.Error.Message)})
				return
			}
			if chunk.Usage != nil {
				u := &Usage{
					InputTokens:  chunk.Usage.PromptTokens,
					OutputTokens: chunk.Usage.CompletionTokens,
				}
				if chunk.Usage.PromptTokensDetails != nil {
					u.CachedInputTokens = chunk.Usage.PromptTokensDetails.CachedTokens
				}
				if chunk.Usage.CompletionTokensDetails != nil {
					u.ReasoningTokens = chunk.Usage.CompletionTokensDetails.ReasoningTokens
				}
				usage = u
			}
			if len(chunk.Choices) > 0 {
				delta := chunk.Choices[0].Delta
				reason := delta.ReasoningContent
				if reason == "" {
					reason = delta.Reasoning
				}
				if reason != "" {
					if !send(ctx, out, Event{Type: Thinking, Delta: reason}) {
						return
					}
				}
				if delta.Content != "" {
					if !send(ctx, out, Event{Type: Text, Delta: delta.Content}) {
						return
					}
				}
			}
		}
		if err := scanner.Err(); err != nil && ctx.Err() == nil {
			send(ctx, out, Event{Type: Error, Err: fmt.Errorf("read paid_api stream: %w", err)})
			return
		}
		if ctx.Err() != nil {
			return
		}
		send(ctx, out, Event{Type: Done, Usage: usage})
	}()
	return out, nil
}

// TestOpenAICompatConnection pings GET {baseURL}/models with the given Bearer key.
func TestOpenAICompatConnection(ctx context.Context, client *http.Client, baseURL, apiKey string) error {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return errors.New("base_url must be a valid https:// URL")
	}
	target := strings.TrimRight(parsed.String(), "/") + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
