package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/dawnim/dawn-server/support/i18n"
	"github.com/prismgo/framework/logger"
)

type operation struct {
	action     string
	reply      chan error
	spec       *Spec
	executable string
}

type service struct {
	name      string
	spec      Spec
	directory string
	socket    string
	commands  chan operation
	mu        sync.RWMutex
	status    Status
	output    io.Writer
	listener  *net.TCPListener
}

type child struct {
	cmd      *exec.Cmd
	done     chan struct{}
	err      error
	started  time.Time
	stopFile string
	lifeline *os.File
	ready    <-chan readiness
	reader   *os.File
	waiting  string
}

var stateLabels = map[string]string{"running": "运行中", "starting": "启动中", "waiting": "等待就绪", "backoff": "等待重试", "stopped": "已停止", "stopping": "停止中"}

func (s *service) snapshot() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

func (s *service) publish(state string, wanted bool, p *child, err error) {
	s.mu.Lock()
	previousState, previousError := s.status.State, s.status.LastError
	s.status.State, s.status.Wanted, s.status.PID = state, wanted, 0
	s.status.Address = s.spec.URL
	if p != nil {
		s.status.PID, s.status.StartedAt = p.cmd.Process.Pid, p.started.UnixMilli()
	}
	if err != nil {
		s.status.LastError = i18n.Message(err, "")
	} else if state == "running" || state == "stopping" || state == "stopped" {
		s.status.LastError = ""
	}
	message := s.status.LastError
	s.mu.Unlock()
	if s.output != nil && (previousState != state || previousError != message) {
		text := i18n.Text("", stateLabels[state])
		if message != "" {
			text += ": " + message
		}
		_, _ = fmt.Fprintln(s.output, text)
	}
}

func (s *service) run(ctx context.Context) {
	var current *child
	var exited <-chan struct{}
	var ready <-chan readiness
	var retry <-chan time.Time
	wanted, backoff := false, time.Second
	defer func() {
		if current != nil {
			s.stop(current)
		}
		if s.listener != nil {
			_ = s.listener.Close()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case op := <-s.commands:
			previous := s.spec
			if op.action == "update" {
				s.spec.Command = slices.Clone(s.spec.Command)
				s.spec.Command[0] = op.executable
				if !wanted {
					op.reply <- nil
					continue
				}
				op.action = "restart"
				if s.spec.Address != "" {
					op.action = "reload"
				}
			}
			if op.action == "attach" {
				op.action = "start"
				if !reflect.DeepEqual(s.spec, *op.spec) {
					if current != nil && s.spec.Address != op.spec.Address {
						op.reply <- i18n.Errorf("监听地址变更请先停止服务")
						continue
					}
					s.spec = *op.spec
					if current != nil {
						op.action = "restart"
						if s.name == "http" || s.name == "ws" {
							op.action = "reload"
						}
					}
				}
			}
			backoff = time.Second
			if op.action == "stop" {
				wanted, retry = false, nil
				s.publish("stopping", false, current, nil)
				if current != nil {
					s.stop(current)
				}
				current, exited, ready = nil, nil, nil
				if s.listener != nil {
					_ = s.listener.Close()
					s.listener = nil
				}
				s.publish("stopped", false, nil, nil)
				op.reply <- nil
				continue
			}
			if op.action == "start" && current != nil {
				op.reply <- nil
				continue
			}
			wanted, retry = true, nil
			if op.action == "restart" && current != nil {
				s.publish("stopping", true, current, nil)
				s.stop(current)
				current, exited, ready = nil, nil, nil
			}
			s.publish("starting", true, current, nil)
			next, err := s.spawn(ctx)
			if err == nil && current != nil && next.waiting != "" {
				s.stop(next)
				err = errors.New(next.waiting)
			}
			if err == nil {
				old := current
				current, exited, ready = next, next.done, next.ready
				if next.waiting != "" {
					s.publish("waiting", true, current, errors.New(next.waiting))
				} else {
					ready = nil
					s.publish("running", true, current, nil)
				}
				if old != nil {
					s.stop(old)
				}
			} else if current == nil {
				retry = time.After(backoff)
				s.publish("backoff", wanted, nil, err)
			} else {
				s.spec = previous
				s.publish("running", wanted, current, err)
			}
			op.reply <- err
		case state := <-ready:
			if state.State == "ready" {
				ready = nil
				s.publish("running", wanted, current, nil)
			} else if state.State == "waiting" {
				s.publish("waiting", wanted, current, errors.New(state.Error))
			} else {
				s.stop(current)
				current, exited, ready = nil, nil, nil
				s.publish("backoff", wanted, nil, errors.New(state.Error))
				retry = time.After(backoff)
			}
		case <-exited:
			err := current.err
			if err == nil {
				err = i18n.Errorf("服务%s意外退出", s.name)
			}
			if time.Since(current.started) > time.Minute {
				backoff = time.Second
			}
			s.stop(current)
			current, exited, ready = nil, nil, nil
			s.publish("backoff", wanted, nil, err)
			logger.Error("Dawn service exited", map[string]any{"service": s.name, "error": err, "retry": backoff.String()})
			retry = time.After(backoff)
			backoff = min(2*backoff, 30*time.Second)
		case <-retry:
			retry = nil
			s.mu.Lock()
			s.status.Restarts++
			s.mu.Unlock()
			s.publish("starting", wanted, nil, nil)
			next, err := s.spawn(ctx)
			if err != nil {
				s.publish("backoff", wanted, nil, err)
				logger.Error("Dawn service retry", map[string]any{"service": s.name, "error": err, "retry": backoff.String()})
				retry = time.After(backoff)
				backoff = min(2*backoff, 30*time.Second)
			} else {
				current, exited, ready = next, next.done, next.ready
				if next.waiting != "" {
					s.publish("waiting", wanted, current, errors.New(next.waiting))
				} else {
					ready = nil
					s.publish("running", wanted, current, nil)
				}
			}
		}
	}
}

func (s *service) spawn(ctx context.Context) (*child, error) {
	if s.spec.Probe != "" {
		if conn, err := net.DialTimeout("tcp", s.spec.Probe, 100*time.Millisecond); err == nil {
			_ = conn.Close()
			return nil, i18n.Errorf("前端端口已被占用: %s", s.spec.Probe)
		}
	}
	if s.spec.Address != "" && s.listener == nil {
		addr, err := net.ResolveTCPAddr("tcp", s.spec.Address)
		if err != nil {
			return nil, err
		}
		s.listener, err = net.ListenTCP("tcp", addr)
		if err != nil {
			return nil, err
		}
	}
	cmd := exec.Command(s.spec.Command[0], s.spec.Command[1:]...)
	// 异常退出后子进程可能仍持有输出管道，限制 Wait 等待以便回收进程组。
	cmd.WaitDelay = time.Second
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = s.spec.Dir, append(os.Environ(), s.spec.Env...), os.Stdout, os.Stderr
	if s.socket != "" {
		cmd.Env = append(cmd.Env, "DAWN_CONTROL_SOCKET="+s.socket)
	}
	if s.output != nil {
		cmd.Stdout, cmd.Stderr = s.output, s.output
	}
	if !s.spec.JoinGroup {
		configureProcess(cmd)
	}
	p := &child{cmd: cmd, done: make(chan struct{}), started: time.Now()}
	if s.name == "horizon" {
		p.stopFile = filepath.Join(s.directory, "horizon-"+strconv.FormatInt(time.Now().UnixNano(), 36)+".stop")
		cmd.Env = append(cmd.Env, "DAWN_HORIZON_STOP_FILE="+p.stopFile)
	}
	if s.listener != nil {
		file, err := s.listener.File()
		if err != nil {
			return nil, err
		}
		defer file.Close()
		cmd.ExtraFiles = append(cmd.ExtraFiles, file)
		cmd.Env = append(cmd.Env, "PRISMGO_HTTP_LISTENER_FD=3")
	}
	if s.spec.Probe == "" {
		reader, writer, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		p.reader = reader
		defer writer.Close()
		cmd.ExtraFiles = append(cmd.ExtraFiles, writer)
		cmd.Env = append(cmd.Env, "DAWN_READY_FD="+strconv.Itoa(2+len(cmd.ExtraFiles)))
		result := make(chan readiness, 1)
		go func() {
			decoder := json.NewDecoder(reader)
			for {
				var state readiness
				if err := decoder.Decode(&state); err != nil {
					state = readiness{State: "failed", Error: err.Error()}
				}
				select {
				case result <- state:
				case <-p.done:
					return
				}
				if state.State != "waiting" {
					return
				}
			}
		}()
		p.ready = result
	}
	parent, lifeline, err := os.Pipe()
	if err != nil {
		if p.reader != nil {
			_ = p.reader.Close()
		}
		close(p.done)
		return nil, err
	}
	defer parent.Close()
	p.lifeline = lifeline
	cmd.ExtraFiles = append(cmd.ExtraFiles, parent)
	cmd.Env = append(cmd.Env, "DAWN_PARENT_FD="+strconv.Itoa(2+len(cmd.ExtraFiles)))
	cmd.Env = append(cmd.Env, "DAWN_STOP_TIMEOUT="+max(s.spec.Timeout, time.Second).String())
	if err := cmd.Start(); err != nil {
		_ = lifeline.Close()
		if p.reader != nil {
			_ = p.reader.Close()
		}
		close(p.done)
		return nil, err
	}
	// 父进程不得持有子进程的管道写端，否则子进程退出无法通过 EOF 被识别。
	if s.spec.Probe == "" {
		_ = cmd.ExtraFiles[len(cmd.ExtraFiles)-2].Close()
	}
	go func() {
		p.err = cmd.Wait()
		if output, ok := s.output.(*logWriter); ok {
			output.Flush()
		}
		close(p.done)
	}()
	timeout := time.NewTimer(30 * time.Second)
	defer timeout.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	err = nil
	for {
		select {
		case state := <-p.ready:
			if state.State == "ready" {
				return p, nil
			}
			if state.State == "waiting" {
				p.waiting = state.Error
				return p, nil
			}
			err = errors.New(state.Error)
		case <-tick.C:
			if s.spec.Probe == "" {
				continue
			}
			conn, probeErr := net.DialTimeout("tcp", s.spec.Probe, 100*time.Millisecond)
			if probeErr != nil {
				continue
			}
			_ = conn.Close()
			select {
			case <-p.done:
				err = p.err
			default:
				return p, nil
			}
		case <-p.done:
			err = p.err
		case <-timeout.C:
			err = i18n.Errorf("服务%s启动超时", s.name)
		case <-ctx.Done():
			err = ctx.Err()
		}
		if err == nil {
			err = i18n.Errorf("服务%s意外退出", s.name)
		}
		s.stop(p)
		return nil, err
	}
}

func (s *service) stop(p *child) {
	if s.spec.JoinGroup {
		_ = terminateProcess(-os.Getpid(), false)
	}
	select {
	case <-p.done:
	default:
		_ = terminateProcess(p.cmd.Process.Pid, false)
		timer := time.NewTimer(max(s.spec.Timeout, time.Second))
		select {
		case <-p.done:
		case <-timer.C:
			group := p.cmd.Process.Pid
			if s.spec.JoinGroup {
				group = os.Getpid()
			}
			_ = terminateProcess(group, true)
			<-p.done
		}
		timer.Stop()
	}
	// 清理属于本次启动的整个进程组，避免 master 或 pnpm 留下子进程。
	if !s.spec.JoinGroup {
		_ = terminateProcess(p.cmd.Process.Pid, true)
	}
	_ = p.lifeline.Close()
	if p.reader != nil {
		_ = p.reader.Close()
	}
	if p.stopFile != "" {
		_ = os.Remove(p.stopFile)
	}
}

// Ready 通过父进程提供的管道报告本服务已经就绪。
func Ready() error {
	return reportReady(readiness{State: "ready"})
}

// WatchParent 在管理器异常消失时通知当前 Application 收尾。
func WatchParent(ctx context.Context, shutdown func(error)) (func(), error) {
	value := os.Getenv("DAWN_PARENT_FD")
	if value == "" {
		return func() {}, nil
	}
	fd, err := strconv.Atoi(value)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "dawn-parent")
	_ = os.Unsetenv("DAWN_PARENT_FD")
	closeOnExec(fd)
	timeout, _ := time.ParseDuration(os.Getenv("DAWN_STOP_TIMEOUT"))
	stopDeadline := context.AfterFunc(ctx, func() {
		time.AfterFunc(timeout, func() { _ = terminateProcess(os.Getpid(), true) })
	})
	go func() {
		_, _ = io.Copy(io.Discard, file)
		if ctx.Err() == nil {
			shutdown(errors.New("Dawn supervisor disconnected"))
		}
	}()
	return func() { stopDeadline(); _ = file.Close() }, nil
}

// Frontend 与 Go 服务使用同样的子进程回收逻辑，由外层管理器决定是否重启。
func Frontend(ctx context.Context, spec Spec) error {
	spec.JoinGroup = true
	s := &service{name: "frontend", spec: spec}
	p, err := s.spawn(ctx)
	if err != nil {
		return err
	}
	defer s.stop(p)
	if err := Ready(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return nil
	case <-p.done:
		return p.err
	}
}
