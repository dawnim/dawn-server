// Package supervisor 管理本项目的服务进程、控制通道和监听资源。
package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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
func Run(ctx context.Context, socket string, specs map[string]Spec, names []string) error {
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
	apply := func(req request) Response {
		result := Response{PID: os.Getpid()}
		names, err := Parse(req.Selection, req.Action)
		if err != nil {
			result.Error = i18n.Message(err, "")
			return result
		}
		if req.Action != "start" && (req.Selection == "all" || req.Selection == "local") {
			names = nil
			for name, service := range services {
				if req.Action != "restart" || service.snapshot().Wanted {
					names = append(names, name)
				}
			}
			slices.Sort(names)
		}
		for _, name := range names {
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
					op := operation{req.Action, make(chan error, 1)}
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
	select {
	case <-ctx.Done():
	case err = <-acceptErr:
	}
	cancel()
	_ = listener.Close()
	workers.Wait()
	<-acceptDone
	connections.Wait()
	return err
}
