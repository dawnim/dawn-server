package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dawnim/dawn-server/support/i18n"
)

// Attach transfers local terminal ownership after the requested services are ready.
// Closing the current owner stops the manager; closing a replaced owner has no effect.
func Attach(ctx context.Context, socket string, specs map[string]Spec, output io.Writer) (bool, error) {
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", socket)
	if err != nil {
		return !socketAbsent(err), err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Minute))
	if err := json.NewEncoder(conn).Encode(request{Selection: strings.Join(slices.Sorted(maps.Keys(specs)), ","), Action: "attach", Specs: specs, PID: os.Getpid()}); err != nil {
		return true, err
	}
	decoder := json.NewDecoder(io.LimitReader(conn, 64<<10))
	var result Response
	if err := decoder.Decode(&result); err != nil {
		return true, err
	}
	if result.Error != "" {
		return true, errors.New(result.Error)
	}
	_ = conn.SetDeadline(time.Time{})
	data, _ := json.MarshalIndent(result, "", "  ")
	_, _ = output.Write(append(data, '\n'))
	err = decoder.Decode(&result)
	if ctx.Err() != nil || result.Replaced {
		return true, nil
	}
	if errors.Is(err, io.EOF) {
		return true, nil
	}
	return true, err
}

// Terminal starts an independent local manager when absent, then owns its control connection.
func Terminal(ctx context.Context, socket string, launcher Spec, specs map[string]Spec, output io.Writer) (result error) {
	if err := os.MkdirAll(filepath.Dir(socket), 0700); err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(filepath.Dir(socket), "local.log"), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	if _, err := log.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	tailCtx, cancel := context.WithCancel(ctx)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-tailCtx.Done():
				return
			case <-tick.C:
				_, _ = io.Copy(output, log)
			}
		}
	}()
	defer func() { cancel(); <-stopped }()
	if connected, err := Attach(ctx, socket, specs, output); connected {
		return err
	}
	cmd := exec.Command(launcher.Command[0], launcher.Command[1:]...)
	cmd.Dir, cmd.Env = launcher.Dir, append(os.Environ(), launcher.Env...)
	// A separate file description keeps the manager's writes independent of the terminal's reader.
	writer, err := os.OpenFile(log.Name(), os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer writer.Close()
	cmd.Stdout, cmd.Stderr = writer, writer
	configureProcess(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		if result != nil {
			_ = cmd.Process.Signal(os.Interrupt)
		}
	}()
	timeout := time.NewTimer(30 * time.Second)
	defer timeout.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if connected, err := Attach(ctx, socket, specs, output); connected {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-done:
			if connected, attachErr := Attach(ctx, socket, specs, output); connected {
				return attachErr
			}
			if err == nil {
				err = i18n.Errorf("本地服务管理器意外退出")
			}
			return err
		case <-timeout.C:
			return i18n.Errorf("本地终端连接超时")
		case <-tick.C:
		}
	}
}
