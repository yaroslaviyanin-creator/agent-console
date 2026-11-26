package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"time"
)

type appRPC func(method string, params any) (json.RawMessage, error)

func (p CodexCliProvider) withAppServer(ctx context.Context, timeout time.Duration, chatConfig bool, use func(appRPC) error) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.Bin, "app-server", "--listen", "stdio://")
	cmd.Env = cliEnvironment()
	if chatConfig {
		if p.home == "" || p.workspace == "" {
			return fmt.Errorf("Codex chat environment is not configured")
		}
		cmd = exec.CommandContext(ctx, p.Bin, "app-server", "--strict-config", "--listen", "stdio://")
		cmd.Dir = p.workspace
		cmd.Env = codexChatEnvironment(p.home)
	}
	var stderr stderrTail
	cmd.Stderr = &stderr
	cmd.WaitDelay = 3 * time.Second
	input, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return stderr.wrap("start Codex app-server", err)
	}
	rpc, err := initializeAppRPC(input, output)
	if err == nil {
		err = use(rpc)
	}
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return stderr.wrap("Codex app-server RPC", err)
	}

	// EOF lets the stdio server exit normally so unexpected exit codes can be reported.
	_ = input.Close()
	shutdownCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	finished := make(chan struct{})
	stopShutdown := context.AfterFunc(shutdownCtx, func() {
		defer close(finished)
		_ = cmd.Process.Kill()
	})
	_, _ = io.Copy(io.Discard, output)
	// Join the shutdown callback before Wait closes the process pipes.
	shutdownTimedOut := !stopShutdown()
	if shutdownTimedOut {
		<-finished
	}
	waitErr := cmd.Wait()
	if err := ctx.Err(); err != nil {
		return stderr.wrap("Codex app-server", err)
	}
	if waitErr != nil && !shutdownTimedOut {
		return stderr.wrap("Codex app-server", waitErr)
	}
	return nil
}

type rpcResponse struct {
	ID     *int            `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func initializeAppRPC(input io.Writer, output io.Reader) (appRPC, error) {
	client := json.NewEncoder(input)
	response := bufio.NewScanner(output)
	response.Buffer(make([]byte, 32<<10), 16<<20)
	id := -1
	request := func(method string, params any) (json.RawMessage, error) {
		id++
		msg := map[string]any{"id": id, "method": method, "params": params}
		if err := client.Encode(msg); err != nil {
			return nil, err
		}
		for response.Scan() {
			var packet rpcResponse
			if len(bytes.TrimSpace(response.Bytes())) == 0 {
				continue
			}
			if err := json.Unmarshal(response.Bytes(), &packet); err != nil {
				return nil, err
			}
			if packet.ID == nil || *packet.ID != id {
				continue
			}
			if len(packet.Error) != 0 && string(packet.Error) != "null" {
				return nil, fmt.Errorf("Codex app-server rejected %s", method)
			}
			return packet.Result, nil
		}
		if err := response.Err(); err != nil {
			return nil, err
		}
		return nil, io.ErrUnexpectedEOF
	}
	initParams := map[string]any{"clientInfo": map[string]string{"name": "agent_console", "version": "0.1.0"}}
	if _, err := request("initialize", initParams); err != nil {
		return nil, err
	}
	if err := client.Encode(map[string]string{"method": "initialized"}); err != nil {
		return nil, err
	}

	return request, nil
}
