package provider

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestProviderImageAttachmentSupportAndSafety(t *testing.T) {
	// 1. Codex stages temp images safely and cleans up via cleanup function, passing -i without enabling tools.
	imgBytes := []byte{0xFF, 0xD8, 0xFF, 0xE0}
	dir, paths, err := stageCodexImages([]ImageAttachment{{Name: "../../evil.jpg", MIMEType: "image/jpeg", Data: imgBytes}})
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || !strings.HasPrefix(paths[0], dir) || strings.Contains(paths[0], "evil") {
		t.Fatalf("unsafe staged path: %v", paths)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0700 {
		t.Fatalf("unexpected temp dir mode: %v", err)
	}
	args, err := codexArgsWithImages(Request{Prompt: "describe"}, paths)
	if err != nil {
		t.Fatal(err)
	}
	idx := slices.Index(args, "-i")
	if idx < 0 || idx+1 >= len(args) || args[idx+1] != paths[0] {
		t.Fatalf("expected -i %q in codex args: %q", paths[0], args)
	}
	if strings.Contains(codexChatConfig, "view_image = true") {
		t.Fatal("view_image must stay disabled in codexChatConfig")
	}
	_ = os.RemoveAll(dir)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("expected temp dir removed, got %v", err)
	}

	// 2. Claude builds stream-json stdin payload with base64 image block while keeping --tools "".
	claudeReq := Request{
		Prompt: "what is this?",
		Images: []ImageAttachment{{Name: "img_0.png", MIMEType: "image/png", Data: []byte("pngdata")}},
	}
	cArgs, err := claudeArgs(claudeReq)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cArgs, "--input-format") || !slices.Contains(cArgs, "stream-json") {
		t.Fatalf("expected --input-format stream-json in claudeArgs: %q", cArgs)
	}
	toolsIdx := slices.Index(cArgs, "--tools")
	if toolsIdx < 0 || cArgs[toolsIdx+1] != "" {
		t.Fatalf("claude --tools must remain empty: %q", cArgs)
	}
	stdinLine, err := claudeStdinPayload(claudeReq)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(stdinLine), &parsed); err != nil || parsed["type"] != "user" {
		t.Fatalf("invalid claude stream-json payload: %s (%v)", stdinLine, err)
	}

	// 3. Antigravity rejects image attachments cleanly in safe chat mode (0 tools).
	_, err = (AntigravityCliProvider{}).Stream(t.Context(), Request{
		UserID: "0123456789abcdef0123456789abcdef",
		Prompt: "look",
		Images: []ImageAttachment{{Name: "a.jpg", MIMEType: "image/jpeg", Data: imgBytes}},
	})
	if !errors.Is(err, ErrUnsupportedImageAttachments) {
		t.Fatalf("expected ErrUnsupportedImageAttachments, got %v", err)
	}
}
