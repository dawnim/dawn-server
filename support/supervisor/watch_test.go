package supervisor

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/prismgo/framework/config"
	"github.com/prismgo/framework/foundation"
)

func TestWatchProcess(t *testing.T) {
	directory := os.Getenv("DAWN_TEST_WATCH")
	if directory == "" {
		return
	}
	config.Add("logging", func() map[string]any {
		return map[string]any{"default": "stderr", "channels": map[string]any{"stderr": map[string]any{"driver": "stderr", "level": "info"}}}
	})
	app := foundation.Configure(directory).Create()
	if err := app.Boot(); err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := Watch(ctx, directory, filepath.Join(directory, "storage"), func(binary string) error {
		output, err := exec.CommandContext(ctx, binary).CombinedOutput()
		fmt.Printf("RELOAD %s|%s\n", binary, strings.TrimSpace(string(output)))
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWatchBuildFailureAndLocalModules(t *testing.T) {
	root := t.TempDir()
	project, dependency := filepath.Join(root, "project"), filepath.Join(root, "value")
	for _, directory := range []string{filepath.Join(project, "app", "admin"), filepath.Join(project, "storage"), dependency} {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for file, contents := range map[string]string{
		filepath.Join(project, "go.mod"):                   "module example.com/watch\n\ngo 1.26\n\nrequire example.com/value v0.0.0\nreplace example.com/value => ../value\n",
		filepath.Join(project, "main.go"):                  "package main\nimport(\"fmt\";\"example.com/watch/app/admin\")\nfunc main(){fmt.Println(admin.Value())}\n",
		filepath.Join(project, "app", "admin", "value.go"): "package admin\nimport \"example.com/value\"\nfunc Value()string{return value.Text+\"before\"}\n",
		filepath.Join(dependency, "go.mod"):                "module example.com/value\n\ngo 1.26\n",
		filepath.Join(dependency, "value.go"):              "package value\nconst Text=\"one\"\n",
	} {
		if err := os.WriteFile(file, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	executable, _ := os.Executable()
	command := exec.Command(executable, "-test.run=^TestWatchProcess$")
	command.Env = append(os.Environ(), "DAWN_TEST_WATCH="+project, "GOWORK=off")
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stdout, command.Stderr = writer, writer
	configureProcess(command)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	t.Cleanup(func() {
		defer reader.Close()
		_ = terminateProcess(command.Process.Pid, false)
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			_ = terminateProcess(command.Process.Pid, true)
			t.Error("watch did not stop")
		}
	})
	lines := make(chan string, 100)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	wait := func(want string) string {
		t.Helper()
		timeout := time.NewTimer(15 * time.Second)
		defer timeout.Stop()
		for {
			select {
			case line, ok := <-lines:
				if !ok {
					t.Fatal("watch exited before " + want)
				}
				if strings.Contains(line, want) {
					return line
				}
			case <-timeout.C:
				t.Fatal("watch timed out waiting for " + want)
			}
		}
	}
	wait("Dawn local watch")
	if err := os.WriteFile(filepath.Join(project, "app", "admin", "value.go"), []byte("package admin\nimport \"example.com/value\"\nfunc Value()string{return value.Text}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	first := wait("RELOAD ")
	if !strings.HasSuffix(first, "|one") {
		t.Fatal(first)
	}
	binary := strings.SplitN(strings.TrimPrefix(first, "RELOAD "), "|", 2)[0]
	if err := os.WriteFile(filepath.Join(dependency, "value.go"), []byte("package value\nsyntax error\n"), 0600); err != nil {
		t.Fatal(err)
	}
	wait("Dawn local build")
	output, err := exec.Command(binary).CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "one" {
		t.Fatalf("failed build replaced working binary: %s %v", output, err)
	}
	if err := os.WriteFile(filepath.Join(dependency, "value.go"), []byte("package value\nconst Text=\"two\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	second := wait("RELOAD ")
	if second != "RELOAD "+binary+"|two" {
		t.Fatal(second)
	}
}

func TestSourceUpdateReloadsAndPreservesStoppedServices(t *testing.T) {
	executable, _ := os.Executable()
	directory := t.TempDir()
	s := &service{
		name: "http", directory: directory, commands: make(chan operation),
		spec: Spec{Command: []string{executable, "-test.run=^TestServiceProcess$"}, Address: "127.0.0.1:0", Timeout: time.Second, Env: []string{"DAWN_TEST_PROCESS=http"}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.run(ctx) }()
	defer func() { cancel(); <-done }()
	apply := func(action, executable string) {
		t.Helper()
		op := operation{action: action, executable: executable, reply: make(chan error, 1)}
		s.commands <- op
		if err := <-op.reply; err != nil {
			t.Fatal(err)
		}
	}
	apply("start", "")
	first := s.snapshot().PID
	apply("update", executable)
	if s.snapshot().PID == first || s.snapshot().State != "running" {
		t.Fatal("updating the same binary path did not reload")
	}
	apply("stop", "")
	next := filepath.Join(directory, "next")
	if err := os.Symlink(executable, next); err != nil {
		t.Fatal(err)
	}
	apply("update", next)
	if status := s.snapshot(); status.Wanted || status.PID != 0 {
		t.Fatal("source update restarted a stopped service")
	}
	if s.spec.Command[0] != next {
		t.Fatal("stopped service did not retain the new binary")
	}
	apply("start", "")
	if s.snapshot().PID == 0 || s.snapshot().State != "running" {
		t.Fatal("stopped service could not start with the new binary")
	}
}
