// Package runnerwire defines the private HTTP interface between core and runner.
package runnerwire

const (
	HealthPath      = "/healthz"
	ProvidersPath   = "/v1/providers"
	StreamPath      = "/v1/stream"
	ModelsPath      = "/v1/models"
	HistoryPath     = "/v1/history"
	HistoryTurnPath = "/v1/history/{id}"
	StreamType      = "application/x-ndjson"
	MaxRequestBytes = 16 << 20
	MaxEventBytes   = 4 << 20
	MaxReplyBytes   = 32 << 20
	MinSecretBytes  = 32
)

type ImageAttachment struct {
	Name     string `json:"name"`
	MIMEType string `json:"mime_type"`
	Data     []byte `json:"data"`
}

type Request struct {
	Provider           string            `json:"provider,omitempty"`
	UserID             string            `json:"user_id"`
	Model              string            `json:"model,omitempty"`
	Reasoning          string            `json:"reasoning,omitempty"`
	Prompt             string            `json:"prompt"`
	SessionID          string            `json:"session_id,omitempty"`
	SystemInstructions string            `json:"system_instructions,omitempty"`
	Ephemeral          bool              `json:"ephemeral,omitempty"`
	Images             []ImageAttachment `json:"images,omitempty"`
	// Reserved for a future authenticated, session-scoped bridge. Reject any
	// nonempty value until that feature exists. Chat never contacts a bridge.
	MCPEndpoint string `json:"mcp_endpoint,omitempty"`
}

type ProvidersResponse struct {
	DefaultProvider string   `json:"default_provider"`
	Providers       []string `json:"providers"`
}

type Usage struct {
	InputTokens       int64 `json:"input_tokens"`
	CachedInputTokens int64 `json:"cached_input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
}

type Event struct {
	Type      string `json:"type"`
	Delta     string `json:"delta,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Usage     *Usage `json:"usage,omitempty"`
	// Error is a generic public message. Provider diagnostics stay in runner logs.
	Error string `json:"error,omitempty"`
}
