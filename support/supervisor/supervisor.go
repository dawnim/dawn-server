// Package supervisor 管理本项目的服务进程、控制通道和监听资源。
package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dawnim/dawn-server/support/i18n"
	"github.com/prismgo/framework/logger"
)

// Spec 声明服务的执行入口；Address 用于继承监听器，Probe 用于前端就绪检查。
type Spec struct {
	Command             []string
	Dir, Address, Probe string
	Env                 []string
	Timeout             time.Duration
	JoinGroup           bool
}

type Status struct {
	Name      string `json:"name"`
	State     string `json:"state"`
	Wanted    bool   `json:"wanted"`
	PID       int    `json:"pid"`
	Restarts  int    `json:"restarts"`
	StartedAt int64  `json:"started_at"`
	LastError string `json:"last_error,omitempty"`
}

// Run 持有排他锁和本地控制入口，直到收到停止命令或应用退出信号。
func Run(ctx context.Context, socket string, specs map[string]Spec, names []string, watch bool) error {
	if err := os.MkdirAll(filepath.Dir(socket), 0700); err != nil {
		return err
	}
	lock, err := lockManager(socket + ".lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := os.Remove(socket); err != nil && !os.IsNotExist(err) {
		return err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := os.Chmod(socket, 0600); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	services := map[string]*service{}
	var workers sync.WaitGroup
	for name, spec := range specs {
		s := &service{name: name, spec: spec, directory: filepath.Dir(socket), commands: make(chan operation), status: Status{Name: name, State: "stopped"}}
		services[name] = s
		workers.Go(func() { s.run(ctx) })
	}
	defer func() { cancel(); _ = listener.Close(); workers.Wait() }()
	var terminalPID atomic.Int64
	apply := func(req request) Response {
		result := Response{PID: os.Getpid(), TerminalPID: int(terminalPID.Load())}
		action := req.Action
		if action == "attach" || action == "update" {
			action = "start"
		}
		names, err := Parse(req.Selection, action)
		if err != nil {
			result.Error = i18n.Message(err, "")
			return result
		}
		if action != "start" && (req.Selection == "all" || req.Selection == "local") {
			names = nil
			for name, service := range services {
				if req.Action != "restart" || service.snapshot().Wanted {
					names = append(names, name)
				}
			}
			slices.Sort(names)
		}
		for _, name := range names {
			if req.Action == "attach" && len(req.Specs[name].Command) == 0 {
				result.Error = i18n.Text("", "服务%s启动入口未配置", name)
				return result
			}
			if services[name] == nil {
				result.Error = i18n.Text("", "未知服务: %s", name)
				return result
			}
		}
		var operations sync.WaitGroup
		var errorsMu sync.Mutex
		for _, name := range names {
			s := services[name]
			if req.Action != "status" {
				operations.Go(func() {
					op := operation{action: req.Action, executable: req.Executable, reply: make(chan error, 1)}
					if req.Action == "attach" {
						spec := req.Specs[name]
						op.spec = &spec
					}
					select {
					case s.commands <- op:
					case <-ctx.Done():
						return
					}
					select {
					case err := <-op.reply:
						if err != nil {
							errorsMu.Lock()
							result.Error += name + ": " + i18n.Message(err, "") + "\n"
							errorsMu.Unlock()
						}
					case <-ctx.Done():
					}
				})
			}
		}
		operations.Wait()
		for _, name := range names {
			result.Services = append(result.Services, services[name].snapshot())
		}
		return result
	}
	var terminalMu sync.Mutex
	var owner net.Conn
	attached := make(chan struct{})
	var attachedOnce sync.Once
	acceptErr := make(chan error, 1)
	acceptDone := make(chan struct{})
	var connections sync.WaitGroup
	go func() {
		defer close(acceptDone)
		for {
			conn, err := listener.Accept()
			if err != nil {
				acceptErr <- err
				return
			}
			connections.Go(func() {
				defer conn.Close()
				stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
				defer stop()
				_ = conn.SetDeadline(time.Now().Add(3 * time.Minute))
				var req request
				if err := json.NewDecoder(io.LimitReader(conn, 16<<10)).Decode(&req); err != nil {
					return
				}
				if req.Action == "attach" {
					terminalMu.Lock()
					result := apply(req)
					if result.Error != "" {
						_ = json.NewEncoder(conn).Encode(result)
						terminalMu.Unlock()
						return
					}
					_ = conn.SetDeadline(time.Time{})
					result.TerminalPID = req.PID
					if err := json.NewEncoder(conn).Encode(result); err != nil {
						terminalMu.Unlock()
						return
					}
					old := owner
					owner = conn
					terminalPID.Store(int64(req.PID))
					attachedOnce.Do(func() { close(attached) })
					if old != nil {
						_ = old.SetWriteDeadline(time.Now().Add(time.Second))
						_ = json.NewEncoder(old).Encode(Response{Replaced: true})
						_ = old.Close()
					}
					terminalMu.Unlock()
					var closed request
					_ = json.NewDecoder(conn).Decode(&closed)
					terminalMu.Lock()
					if owner == conn {
						cancel()
					}
					terminalMu.Unlock()
					return
				}
				result := apply(req)
				_ = json.NewEncoder(conn).Encode(result)
				if req.Action == "stop" && (req.Selection == "all" || req.Selection == "local") {
					cancel()
				}
			})
		}
	}()
	initial := apply(request{Selection: strings.Join(names, ","), Action: "start"})
	logger.Info("Dawn supervisor", map[string]any{"pid": os.Getpid(), "services": initial.Services, "error": initial.Error})
	if initial.Error != "" {
		fmt.Fprintln(os.Stderr, initial.Error)
	}
	if slices.Contains(names, "frontend") {
		select {
		case <-attached:
		case <-time.After(30 * time.Second):
			err = i18n.Errorf("本地终端连接超时")
			cancel()
		case <-ctx.Done():
		case err = <-acceptErr:
			cancel()
		}
	}
	watchErr := make(chan error, 1)
	if watch {
		workers.Go(func() {
			watchErr <- Watch(ctx, specs["http"].Dir, filepath.Dir(socket), func(executable string) error {
				result := apply(request{Selection: "http,ws,horizon,cron", Action: "update", Executable: executable})
				if result.Error != "" {
					return errors.New(result.Error)
				}
				return nil
			})
		})
	}
	select {
	case <-ctx.Done():
	case err = <-acceptErr:
	case err = <-watchErr:
	}
	cancel()
	_ = listener.Close()
	workers.Wait()
	<-acceptDone
	connections.Wait()
	return err
}
