package runner

import (
	"context"
	"github.com/owainlewis/machinist/internal/config"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeAppServerCancellationAndTimeout(t *testing.T) {
	for _, cancelled := range []bool{true, false} {
		t.Run(map[bool]string{true: "cancel", false: "timeout"}[cancelled], func(t *testing.T) {
			directory := t.TempDir()
			script := filepath.Join(directory, "server.sh")
			body := `while IFS= read -r line; do
case "$line" in
*'"id":1,'*) echo '{"id":1,"result":{}}';;
*'"id":2,'*) echo '{"id":2,"result":{"account":{"type":"chatgpt"}}}';;
*'"id":3,'*) echo '{"id":3,"result":{"data":[{"id":"fixture","model":"fixture","isDefault":true}]}}';;
*'"id":4,'*) echo '{"id":4,"result":{"thread":{"id":"primary"}}}';;
*'"id":5,'*) echo '{"method":"item/started","params":{"threadId":"primary","item":{"type":"subAgentActivity","kind":"started","agentThreadId":"child"}}}'; sleep 60 & wait;;
esac
done
`
			if e := os.WriteFile(script, []byte(body), 0600); e != nil {
				t.Fatal(e)
			}
			ctx := context.Background()
			if cancelled {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 300*time.Millisecond)
				defer stop()
			}
			timeout := 300 * time.Millisecond
			if cancelled {
				timeout = time.Minute
			}
			result, e := Execute(ctx, Options{Repository: ".", Prompt: testPrompt, Environment: os.Environ(), DataDirectory: directory, Command: config.ResolvedCommand{Name: "fake", Adapter: "codex-app-server", Command: []string{"sh", script, "app-server"}, Timeout: timeout}, Stdout: io.Discard, Stderr: io.Discard})
			if e == nil {
				t.Fatal("stalled server succeeded")
			}
			want := StateTimedOut
			if cancelled {
				want = StateCancelled
			}
			if result.State != want {
				t.Fatalf("%s: %v", result.State, e)
			}
			raw, _ := os.ReadFile(result.EventsPath)
			if !containsNativeInterrupted(raw) {
				t.Fatalf("missing interrupted child: %s", raw)
			}
		})
	}
}
func containsNativeInterrupted(body []byte) bool {
	return strings.Contains(string(body), `\"status\":\"interrupted\"`)
}
