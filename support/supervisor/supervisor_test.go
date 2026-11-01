package supervisor

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/prismgo/framework/config"
	"github.com/prismgo/framework/foundation"
	prismhttp "github.com/prismgo/framework/http"
)

func init() {
	config.Add("logging", func() map[string]any {
		return map[string]any{"default": "stderr", "channels": map[string]any{"stderr": map[string]any{"driver": "stderr", "level": "error"}}}
	})
}

// TestServiceProcess 在真实子进程中运行，覆盖信号、监听器继承和后代进程清理。
func TestServiceProcess(t *testing.T) {
	mode := os.Getenv("DAWN_TEST_PROCESS")
	if mode == "" {
		return
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer cancel()
	if mode == "tree" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestServiceProcess$")
		cmd.Env = append(os.Environ(), "DAWN_TEST_PROCESS=grandchild")
		if err := cmd.Start(); err != nil {
			os.Exit(2)
		}
		_ = os.WriteFile(os.Getenv("DAWN_TEST_PID_FILE"), []byte(strconv.Itoa(cmd.Process.Pid)), 0600)
	}
	if mode != "http" {
		if mode != "grandchild" {
			_ = Ready()
		}
		<-ctx.Done()
		return
	}
	listener, err := prismhttp.InheritedListener()
	if err != nil || listener == nil {
		os.Exit(3)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			_ = os.WriteFile(os.Getenv("DAWN_TEST_STARTED_FILE"), []byte("started"), 0600)
			time.Sleep(500 * time.Millisecond)
		}
		fmt.Fprint(w, os.Getpid())
	})}
	go server.Serve(listener)
	_ = Ready()
	<-ctx.Done()
	shutdown, done := context.WithTimeout(context.Background(), time.Second)
	defer done()
	_ = server.Shutdown(shutdown)
}

func TestSupervisorLifecycle(t *testing.T) {
	app := foundation.Configure(t.TempDir()).Create()
	if err := app.Boot(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	dir := t.TempDir()
	socket := filepath.Join(dir, "dawn.sock")
	exe, _ := os.Executable()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	startedFile, pidFile := filepath.Join(dir, "started"), filepath.Join(dir, "grandchild")
	specs := map[string]Spec{
		"http": {Command: []string{exe, "-test.run=^TestServiceProcess$"}, Address: address, Timeout: 2 * time.Second,
			Env: []string{"DAWN_TEST_PROCESS=http", "DAWN_TEST_STARTED_FILE=" + startedFile}},
		"cron": {Command: []string{exe, "-test.run=^TestServiceProcess$"}, Timeout: 2 * time.Second,
			Env: []string{"DAWN_TEST_PROCESS=tree", "DAWN_TEST_PID_FILE=" + pidFile}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, socket, specs, []string{"http", "cron"}) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(6 * time.Second):
			t.Error("manager did not stop")
		}
	})
	control := func(name, action string) Response {
		t.Helper()
		result, connected, err := Control(context.Background(), socket, name, action)
		if !connected || err != nil {
			t.Fatalf("%s %s: %v", name, action, err)
		}
		return result
	}
	await := func(check func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if check() {
				return
			}
			time.Sleep(30 * time.Millisecond)
		}
		t.Fatal("state did not converge")
	}
	await(func() bool {
		r, connected, err := Control(ctx, socket, "all", "status")
		if !connected || err != nil || len(r.Services) != 2 {
			return false
		}
		return r.Services[0].State == "running" && r.Services[1].State == "running"
	})
	oldHTTP := control("http", "status").Services[0].PID
	oldCron := control("cron", "status").Services[0].PID
	pid, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	grandchild, _ := strconv.Atoi(string(pid))
	if err := syscall.Kill(oldCron, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	await(func() bool {
		s := control("cron", "status").Services[0]
		return s.State == "running" && s.PID != oldCron && s.Restarts > 0
	})
	if control("http", "status").Services[0].PID != oldHTTP {
		t.Fatal("cron crash restarted HTTP")
	}
	// 被杀 master 的后代不得继续运行（僵尸由操作系统回收，不再执行服务）。
	output, _ := exec.Command("ps", "-p", strconv.Itoa(grandchild), "-o", "stat=").Output()
	if status := strings.TrimSpace(string(output)); status != "" && !strings.HasPrefix(status, "Z") {
		t.Fatalf("orphan process: %s", status)
	}
	control("cron", "stop")
	time.Sleep(1200 * time.Millisecond)
	if s := control("cron", "status").Services[0]; s.PID != 0 || s.Wanted {
		t.Fatal("stopped service was restarted")
	}
	control("cron", "start")
	if control("cron", "status").Services[0].State != "running" {
		t.Fatal("service did not resume")
	}
	client := &http.Client{Timeout: 4 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	slow := make(chan string, 1)
	go func() {
		response, err := client.Get("http://" + address + "/slow")
		if err != nil {
			slow <- err.Error()
			return
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		slow <- string(body)
	}()
	await(func() bool { _, err := os.Stat(startedFile); return err == nil })
	control("http", "reload")
	if got := <-slow; got != strconv.Itoa(oldHTTP) {
		t.Fatalf("in-flight request interrupted: %s", got)
	}
	newHTTP := control("http", "status").Services[0].PID
	if newHTTP == oldHTTP {
		t.Fatal("reload retained old process")
	}
	response, err := client.Get("http://" + address)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(body) != strconv.Itoa(newHTTP) {
		t.Fatalf("wrong generation served: %s", body)
	}
	control("http", "restart")
	if control("http", "status").Services[0].PID == newHTTP {
		t.Fatal("restart retained old process")
	}
	control("all", "stop")
}

func TestReloadFailureKeepsOldProcess(t *testing.T) {
	app := foundation.Configure(t.TempDir()).Create()
	if err := app.Boot(); err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	dir := t.TempDir()
	exe, _ := os.Executable()
	command := filepath.Join(dir, "service")
	// 链接可执行文件，移除链接模拟新版本不可执行；已经运行的旧进程不受影响。
	if err := os.Symlink(exe, command); err != nil {
		t.Fatal(err)
	}
	s := &service{name: "http", spec: Spec{Command: []string{command, "-test.run=^TestServiceProcess$"}, Address: "127.0.0.1:0", Timeout: time.Second,
		Env: []string{"DAWN_TEST_PROCESS=http"}}, directory: dir, commands: make(chan operation), status: Status{Name: "http"}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.run(ctx) }()
	defer func() { cancel(); <-done }()
	call := func(action string) error {
		op := operation{action, make(chan error, 1)}
		s.commands <- op
		return <-op.reply
	}
	if err := call("start"); err != nil {
		t.Fatal(err)
	}
	pid := s.snapshot().PID
	if err := os.Remove(command); err != nil {
		t.Fatal(err)
	}
	if err := call("reload"); err == nil {
		t.Fatal("reload unexpectedly succeeded")
	}
	if s.snapshot().PID != pid || s.snapshot().State != "running" {
		t.Fatal("reload failure stopped old generation")
	}
}

func TestOccupiedPortRecovers(t *testing.T) {
	app := foundation.Configure(t.TempDir()).Create()
	if err := app.Boot(); err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	exe, _ := os.Executable()
	s := &service{name: "http", spec: Spec{Command: []string{exe, "-test.run=^TestServiceProcess$"}, Address: occupied.Addr().String(), Timeout: time.Second,
		Env: []string{"DAWN_TEST_PROCESS=http"}}, commands: make(chan operation), status: Status{Name: "http"}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.run(ctx) }()
	defer func() { cancel(); <-done }()
	op := operation{"start", make(chan error, 1)}
	s.commands <- op
	if err := <-op.reply; err == nil {
		t.Fatal("occupied port was accepted")
	}
	if s.snapshot().State != "backoff" {
		t.Fatal("failed service was not scheduled for retry")
	}
	occupied.Close()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s.snapshot().State == "running" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("service did not recover when port became free")
}
