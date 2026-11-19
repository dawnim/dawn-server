package cmd_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	dawn "github.com/dawnim/dawn-server"
	"github.com/prismgo/framework/foundation"
)

func TestRenameCommand(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GOWORK", "off")
	files := map[string]string{
		"go.mod": "module example.com/old-app\n\ngo 1.26.0\n",
		"main.go": `package main
import (
    "fmt"
    "example.com/old-app/internal/application"
)
func main() { fmt.Print(application.Name) }
`,
		"internal/application/app.go": `package application
const Name = "old-app"
const Module = "example.com/old-app"
const Other = "example.com/old-app-extra/pkg"
`,
		".env.example":            "APP_NAME=old-app\r\nCACHE_PREFIX=old-app:local\r\n# REDIS_QUEUE_PREFIX=old-app:local:queue\r\nDB_PASSWORD=old-app:secret\r\nAPP_KEY=old-app:key\r\n",
		".env":                    "APP_NAME=old-app\nSESSION_PREFIX=old-app:local:session\nDB_PASSWORD=old-app:secret\nAPP_KEY=old-app:key\n",
		"admin/package.json":      "{\n  \"name\": \"old-app-admin\",\n  \"private\": true\n}\n",
		"supervisord.conf":        "[group:old-app]\ncommand=bin/old-app dawn:start all\n",
		"docs/example.go":         `package example; const Name = "old-app"`,
		"node_modules/example.go": `package example; const Name = "old-app"`,
	}
	for name, content := range files {
		file := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	app := foundation.Configure(root).WithExtensionProviders(dawn.ServiceProvider{}).Create()
	if err := app.Boot(); err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	// Renaming a freshly cloned application does not resolve database or Redis services.
	for i := 0; i < 2; i++ {
		if err := app.NewConsoleKernel().RunContextArgv(context.Background(), []string{"test", "dawn:rename", "example.com/new-app"}); err != nil {
			t.Fatal(err)
		}
	}
	for file, parts := range map[string][]string{
		"go.mod":                      {"module example.com/new-app"},
		"main.go":                     {`"example.com/new-app/internal/application"`},
		"internal/application/app.go": {`"new-app"`, `"example.com/new-app"`, `"example.com/old-app-extra/pkg"`},
		".env.example":                {"APP_NAME=new-app\r\n", "CACHE_PREFIX=new-app:local\r\n", "# REDIS_QUEUE_PREFIX=new-app:local:queue\r\n", "DB_PASSWORD=old-app:secret", "APP_KEY=old-app:key"},
		".env":                        {"APP_NAME=new-app\n", "SESSION_PREFIX=new-app:local:session\n", "DB_PASSWORD=old-app:secret", "APP_KEY=old-app:key"},
		"admin/package.json":          {`"name": "new-app-admin"`},
		"supervisord.conf":            {"[group:new-app]", "bin/new-app dawn:start all"},
		"docs/example.go":             {`"old-app"`},
		"node_modules/example.go":     {`"old-app"`},
	} {
		data, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatal(err)
		}
		for _, part := range parts {
			if !strings.Contains(string(data), part) {
				t.Fatalf("%s missing %q: %s", file, part, data)
			}
		}
	}
	command := exec.Command("go", "run", ".")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil || string(output) != "new-app" {
		t.Fatal("renamed application did not compile and run", string(output), err)
	}
}
