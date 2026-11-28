package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"agent-console/server/internal/runnerwire"
)

const (
	remoteRunnerHeaderTimeout = 15 * time.Second
	remoteRunnerReplyTimeout  = 30 * time.Second
)

// RemoteRunnerProvider calls an isolated remote runner over authenticated HTTP.
type RemoteRunnerProvider struct {
	baseURL  *url.URL
	secret   string
	client   *http.Client
	provider string
}

var _ Provider = (*RemoteRunnerProvider)(nil)

// NewRemoteRunnerProvider validates the runner base URL and shared secret and
// constructs a provider with proxy-free, non-redirecting HTTP transport.
func NewRemoteRunnerProvider(rawURL, secret string) (*RemoteRunnerProvider, error) {
	baseURL, err := validateRunnerURL(rawURL)
	if err != nil {
		return nil, err
	}
	if err := validateRunnerSecret(secret); err != nil {
		return nil, err
	}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: remoteRunnerHeaderTimeout,
		ExpectContinueTimeout: 1 * time.Second,
	}
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &RemoteRunnerProvider{
		baseURL: baseURL,
		secret:  secret,
		client:  client,
	}, nil
}

func validateRunnerURL(rawURL string) (*url.URL, error) {
	if rawURL == "" {
		return nil, errors.New("remote runner URL is required")
	}
	if strings.ContainsAny(rawURL, "?#") {
		return nil, errors.New("remote runner URL must not include query or fragment")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, errors.New("invalid remote runner URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("remote runner URL must use http or https")
	}
	if parsed.Opaque != "" || parsed.User != nil {
		return nil, errors.New("remote runner URL must not include credentials")
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return nil, errors.New("remote runner URL must include host")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" {
		return nil, errors.New("remote runner URL must not include query or fragment")
	}
	return parsed, nil
}

func validateRunnerSecret(secret string) error {
	if len(secret) < runnerwire.MinSecretBytes {
		return errors.New("remote runner secret is shorter than minimum length")
	}
	for i := 0; i < len(secret); i++ {
		b := secret[i]
		// Restrict to printable non-whitespace ASCII so Bearer headers cannot carry
		// control characters, newlines, or ambiguous whitespace.
		if b <= 0x20 || b >= 0x7f {
			return errors.New("remote runner secret contains invalid header characters")
		}
	}
	return nil
}

// ForProvider returns a handle bound to a specific runner provider ID while
// sharing the underlying HTTP client and connection pool.
func (p *RemoteRunnerProvider) ForProvider(providerName string) *RemoteRunnerProvider {
	if p == nil {
		return nil
	}
	bound := *p
	bound.provider = strings.TrimSpace(providerName)
	return &bound
}

func (p *RemoteRunnerProvider) endpointURL(endpointPath, rawQuery string) string {
	u := p.baseURL.JoinPath(strings.TrimPrefix(endpointPath, "/"))
	u.RawQuery = rawQuery
	return u.String()
}

func (p *RemoteRunnerProvider) newRequest(ctx context.Context, method, endpointPath, rawQuery string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, p.endpointURL(endpointPath, rawQuery), body)
	if err != nil {
		return nil, errors.New("invalid remote runner request")
	}
	req.Header.Set("Authorization", "Bearer "+p.secret)
	return req, nil
}

// Stream sends a single prompt turn to the remote runner and streams NDJSON events.
func (p *RemoteRunnerProvider) Stream(ctx context.Context, req Request) (<-chan Event, error) {
	if p == nil || p.client == nil || p.baseURL == nil {
		return nil, errors.New("remote runner is not configured")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var wireImages []runnerwire.ImageAttachment
	if len(req.Images) > 0 {
		wireImages = make([]runnerwire.ImageAttachment, 0, len(req.Images))
		for _, img := range req.Images {
			wireImages = append(wireImages, runnerwire.ImageAttachment{
				Name:     img.Name,
				MIMEType: img.MIMEType,
				Data:     img.Data,
			})
		}
	}
	wireReq := runnerwire.Request{
		Provider:           p.provider,
		UserID:             req.UserID,
		Model:              req.Model,
		Reasoning:          req.Reasoning,
		Prompt:             req.Prompt,
		SessionID:          req.SessionID,
		SystemInstructions: req.SystemInstructions,
		Ephemeral:          req.Ephemeral,
		Images:             wireImages,
	}
	payload, err := json.Marshal(wireReq)
	if err != nil {
		return nil, errors.New("encode remote runner request")
	}
	if len(payload) > runnerwire.MaxRequestBytes {
		return nil, errors.New("remote runner request exceeds size limit")
	}
	httpReq, err := p.newRequest(ctx, http.MethodPost, runnerwire.StreamPath, "", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", runnerwire.StreamType)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, errors.New("remote runner request failed")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusBadRequest && bytes.Contains(body, []byte(ErrUnsupportedImageAttachments.Error())) {
			return nil, ErrUnsupportedImageAttachments
		}
		return nil, fmt.Errorf("remote runner returned status %d", resp.StatusCode)
	}

	out := make(chan Event)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		stopCancel := context.AfterFunc(ctx, func() {
			_ = resp.Body.Close()
		})
		defer stopCancel()

		p.readStream(ctx, resp.Body, out)
	}()
	return out, nil
}

func (p *RemoteRunnerProvider) readStream(ctx context.Context, body io.Reader, out chan<- Event) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 32<<10), runnerwire.MaxEventBytes)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var wireEvent runnerwire.Event
		if err := json.Unmarshal(line, &wireEvent); err != nil {
			if ctx.Err() == nil {
				send(ctx, out, Event{Type: Error, Err: errors.New("malformed remote runner event")})
			}
			return
		}
		event, terminal, ok := mapWireEvent(wireEvent)
		if !ok {
			if ctx.Err() == nil {
				send(ctx, out, Event{Type: Error, Err: errors.New("unknown remote runner event")})
			}
			return
		}
		if !send(ctx, out, event) || terminal {
			return
		}
	}

	if ctx.Err() != nil {
		return
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			send(ctx, out, Event{Type: Error, Err: errors.New("remote runner response too large")})
			return
		}
		send(ctx, out, Event{Type: Error, Err: errors.New("failed to read remote runner stream")})
		return
	}
	send(ctx, out, Event{Type: Error, Err: errors.New("remote runner stream ended before terminal event")})
}

func mapWireEvent(wireEvent runnerwire.Event) (event Event, terminal bool, ok bool) {
	var usage *Usage
	if wireEvent.Usage != nil {
		u := Usage{
			InputTokens:       wireEvent.Usage.InputTokens,
			CachedInputTokens: wireEvent.Usage.CachedInputTokens,
			OutputTokens:      wireEvent.Usage.OutputTokens,
			ReasoningTokens:   wireEvent.Usage.ReasoningTokens,
		}
		usage = &u
	}
	switch wireEvent.Type {
	case Thinking, Text:
		return Event{
			Type:      wireEvent.Type,
			Delta:     wireEvent.Delta,
			SessionID: wireEvent.SessionID,
			Usage:     usage,
		}, false, true
	case Done:
		return Event{
			Type:      Done,
			Delta:     wireEvent.Delta,
			SessionID: wireEvent.SessionID,
			Usage:     usage,
		}, true, true
	case Error:
		msg := strings.TrimSpace(wireEvent.Error)
		if msg == "" {
			msg = "remote runner failed"
		}
		return Event{
			Type: Error,
			Err:  errors.New(msg),
		}, true, true
	default:
		return Event{}, false, false
	}
}

// ListModels fetches the model catalog from the remote runner.
func (p *RemoteRunnerProvider) ListModels(ctx context.Context) ([]Model, error) {
	rawQuery := ""
	if p != nil && p.provider != "" {
		rawQuery = "provider=" + url.QueryEscape(p.provider)
	}
	return getRunnerSlice[Model](ctx, p, runnerwire.ModelsPath, rawQuery)
}

// ListProviders fetches the enabled providers and default provider from the remote runner.
func (p *RemoteRunnerProvider) ListProviders(ctx context.Context) (runnerwire.ProvidersResponse, error) {
	return getRunnerJSON[runnerwire.ProvidersResponse](ctx, p, runnerwire.ProvidersPath, "")
}

// ListIndexedChats fetches the indexed chat list from the remote runner.
func (p *RemoteRunnerProvider) ListIndexedChats(ctx context.Context) ([]IndexedChat, error) {
	return getRunnerSlice[IndexedChat](ctx, p, runnerwire.HistoryPath, "")
}

// ReadIndexedTurns fetches the turns of a single indexed chat after validating its UUID.
func (p *RemoteRunnerProvider) ReadIndexedTurns(ctx context.Context, id string) ([]IndexedTurn, error) {
	if !sessionPattern.MatchString(id) {
		return nil, errors.New("invalid indexed chat ID")
	}
	return getRunnerSlice[IndexedTurn](ctx, p, runnerwire.HistoryPath+"/"+id, "")
}

func getRunnerSlice[T any](ctx context.Context, p *RemoteRunnerProvider, endpointPath, rawQuery string) ([]T, error) {
	items, err := getRunnerJSON[[]T](ctx, p, endpointPath, rawQuery)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []T{}
	}
	return items, nil
}

func getRunnerJSON[T any](ctx context.Context, p *RemoteRunnerProvider, endpointPath, rawQuery string) (T, error) {
	var zero T
	if p == nil || p.client == nil || p.baseURL == nil {
		return zero, errors.New("remote runner is not configured")
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, remoteRunnerReplyTimeout)
		defer cancel()
	}

	httpReq, err := p.newRequest(ctx, http.MethodGet, endpointPath, rawQuery, nil)
	if err != nil {
		return zero, err
	}
	httpReq.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return zero, ctxErr
		}
		return zero, errors.New("remote runner request failed")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return zero, fmt.Errorf("remote runner returned status %d", resp.StatusCode)
	}

	stopCancel := context.AfterFunc(ctx, func() {
		_ = resp.Body.Close()
	})
	defer stopCancel()

	data, err := io.ReadAll(io.LimitReader(resp.Body, runnerwire.MaxReplyBytes+1))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return zero, ctxErr
		}
		return zero, errors.New("failed to read remote runner response")
	}
	if int64(len(data)) > runnerwire.MaxReplyBytes {
		return zero, errors.New("remote runner response too large")
	}

	var decoded T
	if err := json.Unmarshal(data, &decoded); err != nil {
		return zero, errors.New("malformed remote runner response")
	}
	return decoded, nil
}
