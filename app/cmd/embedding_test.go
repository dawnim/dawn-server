package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prismgo/framework/config"
	"github.com/prismgo/framework/console"
	"github.com/prismgo/framework/foundation"
)

func TestEmbeddingProcess(t *testing.T) {
	if os.Getenv("DAWN_EMBEDDING_TEST_HELPER") != "1" {
		return
	}
	marker := os.Getenv("DAWN_EMBEDDING_TEST_MARKER")
	data, _ := json.Marshal(map[string]string{"model": os.Getenv("EMBEDDING_MODEL"), "cache": os.Getenv("HF_HOME"), "device": os.Getenv("EMBEDDING_DEVICE")})
	if err := os.WriteFile(marker, data, 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:"+os.Getenv("DAWN_EMBEDDING_TEST_PORT"))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"ready":true}`) })}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	<-ctx.Done()
	_ = server.Close()
	<-done
}

func TestEmbeddingManagedProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test executable launcher uses a POSIX shell")
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, ".env"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	toolDir := filepath.Join(directory, "embedding")
	config.Add("dawn", func() map[string]any {
		return map[string]any{"embedding": map[string]any{"directory": toolDir, "port": port, "model": "test/model", "device": "cpu", "python": filepath.Join(directory, "python-bootstrap")}}
	})
	app := foundation.Configure(directory).WithCommands(func() console.Command { return &EmbeddingCommand{} }).Create()
	if err = app.Boot(); err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	if err = runEmbedding(context.Background()); err == nil || !strings.Contains(err.Error(), "install") {
		t.Fatal("missing environment accepted", err)
	}
	installer := filepath.Join(directory, "python-bootstrap")
	script := `#!/bin/sh
if [ "$2" = "venv" ]; then
  mkdir -p "$3/bin"
  printf '#!/bin/sh\necho installed > "$(dirname "$0")/../../pip-installed"\n' > "$3/bin/python"
  chmod +x "$3/bin/python"
fi
`
	if err = os.WriteFile(installer, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	if err = app.NewConsoleKernel().RunContextArgv(context.Background(), []string{"test", "dawn:embedding", "install"}); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(toolDir, "pip-installed")); err != nil {
		t.Fatal("dependency installer was not invoked", err)
	}
	_, python := embeddingPaths()
	if err = os.MkdirAll(filepath.Dir(python), 0755); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	launcher := "#!/bin/sh\nexec '" + strings.ReplaceAll(executable, "'", "'\\''") + "' -test.run=^TestEmbeddingProcess$ -- \"$@\"\n"
	if err = os.WriteFile(python, []byte(launcher), 0755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(directory, "environment.json")
	t.Setenv("DAWN_EMBEDDING_TEST_HELPER", "1")
	t.Setenv("DAWN_EMBEDDING_TEST_MARKER", marker)
	t.Setenv("DAWN_EMBEDDING_TEST_PORT", strconv.Itoa(port))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runEmbedding(ctx) }()
	defer func() { cancel() }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		response, e := http.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/health")
		if e == nil {
			_ = response.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("embedding did not become ready", e)
		}
		time.Sleep(25 * time.Millisecond)
	}
	time.Sleep(550 * time.Millisecond)
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	var environment map[string]string
	if err = json.Unmarshal(data, &environment); err != nil {
		t.Fatal(err)
	}
	if environment["model"] != "test/model" || environment["cache"] != filepath.Join(toolDir, "models") {
		t.Fatal("incorrect runtime environment", environment)
	}
	if _, err = os.Stat(filepath.Join(toolDir, "main.py")); err != nil {
		t.Fatal("embedded tools were not published", err)
	}
	cancel()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("embedding child did not stop")
	}
}
