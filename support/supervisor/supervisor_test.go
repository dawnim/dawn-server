package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
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
	if mode == "fail" {
		fmt.Fprintln(os.Stderr, "manager-startup-failed")
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer cancel()
	if mode == "manager" {
		app := foundation.Configure(t.TempDir()).Create()
		if err := app.Boot(); err != nil {
			t.Fatal(err)
		}
		defer app.Close()
		var specs map[string]Spec
		if err := json.Unmarshal([]byte(os.Getenv("DAWN_TEST_SPECS")), &specs); err != nil {
			t.Fatal(err)
		}
		if err := Run(ctx, os.Getenv("DAWN_TEST_SOCKET"), specs, slices.Sorted(maps.Keys(specs)), false, &Display{Out: os.Stdout, Locale: "zh"}); err != nil {
			t.Fatal(err)
		}
		return
	}
	if socket := os.Getenv("DAWN_CONTROL_SOCKET"); socket != "" {
		done := make(chan struct{})
		go func() { defer close(done); ReportRuntime(ctx, socket) }()
		defer func() { cancel(); <-done }()
	}
	if text := os.Getenv("DAWN_TEST_STDOUT"); text != "" {
		fmt.Println(text)
	}
	if file := os.Getenv("DAWN_TEST_WAIT_FILE"); file != "" {
		app := foundation.Configure(t.TempDir()).Create()
		if err := app.Boot(); err != nil {
			t.Fatal(err)
		}
		defer app.Close()
		if err := Wait(ctx, func(context.Context) error { _, err := os.Stat(file); return err }); err != nil {
			return
		}
	}
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

func TestSupervisorWaitAndRecover(t *testing.T) {
	app := foundation.Configure(t.TempDir()).Create()
	if err := app.Boot(); err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	dir := t.TempDir()
	socket, barrier := filepath.Join(dir, "dawn.sock"), filepath.Join(dir, "ready")
	exe, _ := os.Executable()
	specs := map[string]Spec{"cron": {Command: []string{exe, "-test.run=^TestServiceProcess$"}, Timeout: time.Second, Env: []string{"DAWN_TEST_PROCESS=idle", "DAWN_TEST_WAIT_FILE=" + barrier}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, socket, specs, []string{"cron"}, false, &Display{Out: io.Discard, Locale: "zh"})
	}()
	var waiting Status
	var last Response
	var lastError error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r, _, err := Control(ctx, socket, "cron", "status")
		last, lastError = r, err
		if err == nil && len(r.Services) == 1 && r.Services[0].State == "waiting" && r.Services[0].Process != nil && r.Services[0].Process.Goroutines != nil {
			waiting = r.Services[0]
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if waiting.PID == 0 || waiting.LastError == "" {
		select {
		case err := <-done:
			t.Fatal("manager stopped", err)
		default:
		}
		t.Fatal("waiting service or runtime metrics not visible", waiting, last, lastError)
	}
	if err := os.WriteFile(barrier, []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(7 * time.Second)
	var running Status
	for time.Now().Before(deadline) {
		r, _, err := Control(ctx, socket, "cron", "status")
		if err == nil && len(r.Services) == 1 && r.Services[0].State == "running" {
			running = r.Services[0]
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if running.PID != waiting.PID || running.Restarts != 0 || running.LastError != "" {
		t.Fatal("recovery restarted service", waiting, running)
	}
	if err := os.Remove(barrier); err != nil {
		t.Fatal(err)
	}
	r, _, err := Control(ctx, socket, "cron", "restart")
	if err != nil || len(r.Services) != 1 || r.Services[0].State != "waiting" {
		t.Fatal("service did not wait on restart", r, err)
	}
	waitingPID := r.Services[0].PID
	r, _, err = Control(ctx, socket, "cron", "stop")
	if err != nil || r.Services[0].Wanted || r.Services[0].State != "stopped" || r.Services[0].LastError != "" || syscall.Kill(waitingPID, 0) == nil {
		t.Fatal("waiting process was not stopped", r, err)
	}
	if err := os.WriteFile(barrier, []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	r, _, err = Control(ctx, socket, "cron", "status")
	if err != nil || r.Services[0].PID != 0 || r.Services[0].Wanted {
		t.Fatal("stopped service was resumed", r, err)
	}
	_, _, _ = Control(ctx, socket, "all", "stop")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("manager did not stop")
	}
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
	go func() {
		done <- Run(ctx, socket, specs, []string{"http", "cron"}, false, &Display{Out: io.Discard, Locale: "zh"})
	}()
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
	barrier := filepath.Join(dir, "ready")
	if err := os.WriteFile(barrier, []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	// 链接可执行文件，移除链接模拟新版本不可执行；已经运行的旧进程不受影响。
	if err := os.Symlink(exe, command); err != nil {
		t.Fatal(err)
	}
	s := &service{name: "http", spec: Spec{Command: []string{command, "-test.run=^TestServiceProcess$"}, Address: "127.0.0.1:0", Timeout: time.Second,
		Env: []string{"DAWN_TEST_PROCESS=http", "DAWN_TEST_WAIT_FILE=" + barrier}}, directory: dir, commands: make(chan operation), status: Status{Name: "http"}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); s.run(ctx) }()
	defer func() { cancel(); <-done }()
	call := func(action string) error {
		op := operation{action: action, reply: make(chan error, 1)}
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
	if err := os.Symlink(exe, command); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(barrier); err != nil {
		t.Fatal(err)
	}
	if err := call("reload"); err == nil || s.snapshot().PID != pid || s.snapshot().State != "running" {
		t.Fatal("waiting candidate replaced old generation", s.snapshot(), err)
	}
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	response, err := client.Get("http://" + s.listener.Addr().String())
	if err != nil {
		t.Fatal("old generation stopped serving", err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(body) != strconv.Itoa(pid) {
		t.Fatalf("wrong generation served: %s", body)
	}
	if err := os.WriteFile(barrier, []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := call("reload"); err != nil || s.snapshot().PID == pid || s.snapshot().State != "running" {
		t.Fatal("recovered candidate did not take over", s.snapshot(), err)
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
	op := operation{action: "start", reply: make(chan error, 1)}
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

func TestTerminalFailureLog(t *testing.T) {
	var output bytes.Buffer
	exe, _ := os.Executable()
	err := Terminal(context.Background(), filepath.Join(t.TempDir(), "dawn.sock"), Spec{
		Command: []string{exe, "-test.run=^TestServiceProcess$"}, Env: []string{"DAWN_TEST_PROCESS=fail"},
	}, nil, &Display{Out: &output, Locale: "zh"})
	if err == nil || !strings.Contains(output.String(), "manager-startup-failed") {
		t.Fatalf("startup error log missing: %v %q", err, output.String())
	}
}

func TestTerminalLockCancelled(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "dawn.sock")
	lock, err := lockManager(socket + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	err = Terminal(ctx, socket, Spec{Command: []string{"must-not-launch"}}, nil, &Display{Out: io.Discard, Locale: "zh"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("lock wait did not respect cancellation", err)
	}
}

func TestTerminalTakeover(t *testing.T) {
	app := foundation.Configure(t.TempDir()).Create()
	if err := app.Boot(); err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	dir := t.TempDir()
	socket := filepath.Join(dir, "dawn.sock")
	exe, _ := os.Executable()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	specs := map[string]Spec{
		"http": {Command: []string{exe, "-test.run=^TestServiceProcess$"}, Address: address, Timeout: 2 * time.Second, Env: []string{"DAWN_TEST_PROCESS=http", "DAWN_TEST_STDOUT=terminal-log", "DAWN_TEST_STARTED_FILE=" + filepath.Join(dir, "started")}},
		"cron": {Command: []string{exe, "-test.run=^TestServiceProcess$"}, Timeout: 2 * time.Second, Env: []string{"DAWN_TEST_PROCESS=idle"}},
	}
	raw, _ := json.Marshal(specs)
	launcher := Spec{Command: []string{exe, "-test.run=^TestServiceProcess$"}, Dir: dir, Env: []string{"DAWN_TEST_PROCESS=manager", "DAWN_TEST_SPECS=" + string(raw), "DAWN_TEST_SOCKET=" + socket}}
	// A closing manager may retain its lock after its control socket disappears.
	lock, err := lockManager(socket + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	firstCtx, firstCancel := context.WithCancel(context.Background())
	defer firstCancel()
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- Terminal(firstCtx, socket, launcher, specs, &Display{Out: io.Discard, Locale: "zh"})
	}()
	select {
	case err := <-firstDone:
		t.Fatal("terminal did not wait for the closing manager", err)
	case <-time.After(250 * time.Millisecond):
	}
	_ = lock.Close()
	var before Response
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		before, _, err = Control(context.Background(), socket, "all", "status")
		if err == nil && before.TerminalPID != 0 && len(before.Services) == 2 && before.Services[0].State == "running" && before.Services[1].State == "running" {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if err != nil || len(before.Services) != 2 {
		t.Fatal("manager not ready", before, err)
	}
	t.Cleanup(func() { _, _, _ = Control(context.Background(), socket, "all", "stop") })
	// An unsuccessful generation must leave the original terminal and HTTP process running.
	failed := maps.Clone(specs)
	failedSpec := failed["http"]
	failedSpec.Command = []string{filepath.Join(dir, "missing")}
	failed["http"] = failedSpec
	if _, err := Attach(context.Background(), socket, failed, &Display{Out: io.Discard, Locale: "zh"}); err == nil {
		t.Fatal("failed generation accepted")
	}
	select {
	case err := <-firstDone:
		t.Fatal("failed takeover detached old owner", err)
	default:
	}
	next := maps.Clone(specs)
	for name, spec := range next {
		spec.Env = append(slices.Clone(spec.Env), "DAWN_TEST_GENERATION=2")
		next[name] = spec
	}
	secondCtx, secondCancel := context.WithCancel(context.Background())
	defer secondCancel()
	secondDone := make(chan error, 1)
	go func() {
		_, err := Attach(secondCtx, socket, next, &Display{Out: io.Discard, Locale: "zh"})
		secondDone <- err
	}()
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("old terminal did not exit")
	}
	firstCancel()
	after, connected, err := Control(context.Background(), socket, "all", "status")
	if !connected || err != nil || after.PID != before.PID {
		t.Fatal("manager was replaced instead of terminal", after, err)
	}
	for i, service := range after.Services {
		if service.State != "running" || service.PID == before.Services[i].PID {
			t.Fatal("new code not loaded", before, after)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, "local-cli.log"))
	if err != nil || !strings.Contains(string(data), "terminal-log") {
		t.Fatal("logs not retained", err)
	}
	secondCancel()
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("new terminal did not exit")
	}
	deadline = time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if _, connected, _ := Control(context.Background(), socket, "all", "status"); !connected {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("current terminal exit did not stop manager")
}
