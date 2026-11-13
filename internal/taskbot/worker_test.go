package taskbot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexArguments(t *testing.T) {
	args := strings.Join(CodexArguments("/tmp/work", "/tmp/schema", "/tmp/output", ""), " ")
	for _, required := range []string{"--ignore-user-config", "--sandbox read-only", "--disable shell_tool", "--ephemeral", "--output-schema"} {
		if !strings.Contains(args, required) {
			t.Fatal(args)
		}
	}
	if strings.Contains(args, "dangerously") {
		t.Fatal(args)
	}
}
func TestWorkerRejectsUnauthorizedAndOversizedRequests(t *testing.T) {
	called := false
	handler := WorkerHandler("secret", func(context.Context, Payload) (Proposal, error) { called = true; return Proposal{}, nil })
	r := httptest.NewRequest(http.MethodPost, "/propose", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || called {
		t.Fatal(w.Code)
	}
	r = httptest.NewRequest(http.MethodPost, "/propose", strings.NewReader(strings.Repeat("x", 1_000_001)))
	r.Header.Set("Authorization", "Bearer secret")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code < 400 || called {
		t.Fatal(w.Code)
	}
}

func TestTimeoutKillsLauncherChildren(t *testing.T) {
	directory := t.TempDir()
	marker := filepath.Join(directory, "child-finished")
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	command := isolatedCommand(ctx, "sh", "-c", `(sleep 0.2; echo leaked > "$1") & wait`, "test", marker)
	if err := command.Run(); err == nil {
		t.Fatal("expected timeout")
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("launcher child survived cancellation")
	}
}
