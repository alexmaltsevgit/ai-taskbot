package taskbot

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func CodexArguments(workdir, schema, output, model string) []string {
	args := []string{"exec", "--ignore-user-config", "--ignore-rules", "--ephemeral", "--skip-git-repo-check", "--sandbox", "read-only", "--disable", "shell_tool", "--disable", "apps", "--disable", "multi_agent", "-c", `web_search="disabled"`, "--cd", workdir, "--output-schema", schema, "--output-last-message", output}
	if model != "" {
		args = append(args, "--model", model)
	}
	return append(args, "-")
}
func InferWithCodex(ctx context.Context, payload Payload) (Proposal, error) {
	directory, err := os.MkdirTemp("", "ai-taskbot-")
	if err != nil {
		return Proposal{}, err
	}
	defer os.RemoveAll(directory)
	schema, output := filepath.Join(directory, "schema.json"), filepath.Join(directory, "result.json")
	if err = os.WriteFile(schema, []byte(ProposalSchema), 0600); err != nil {
		return Proposal{}, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Proposal{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	command := isolatedCommand(ctx, "codex", CodexArguments(directory, schema, output, os.Getenv("CODEX_MODEL"))...)
	command.Env = CodexEnvironment()
	command.Stdin = strings.NewReader(SystemPrompt + "\nUNTRUSTED INPUT JSON:\n" + string(encoded))
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.WaitDelay = 5 * time.Second
	if err = command.Run(); err != nil {
		return Proposal{}, fmt.Errorf("Codex invocation unavailable")
	}
	info, err := os.Stat(output)
	if err != nil || info.Size() > 65536 {
		return Proposal{}, fmt.Errorf("invalid model output")
	}
	raw, err := os.ReadFile(output)
	if err != nil {
		return Proposal{}, err
	}
	var result Proposal
	if err = json.Unmarshal(raw, &result); err != nil {
		return Proposal{}, fmt.Errorf("invalid model JSON")
	}
	return result, nil
}
func WorkerHandler(token string, infer func(context.Context, Payload) (Proposal, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/propose" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1_000_000)
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Invalid request size", http.StatusRequestEntityTooLarge)
			return
		}
		var payload Payload
		if err = json.Unmarshal(raw, &payload); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		result, err := infer(r.Context(), payload)
		if err != nil {
			http.Error(w, "Inference unavailable; check login and subscription limits", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})
}

// npm launches the native Codex executable as a child. Cancel the whole Linux
// process group so timeouts do not leave inference running in the background.
func isolatedCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, name, args...)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	command.WaitDelay = 5 * time.Second
	return command
}
