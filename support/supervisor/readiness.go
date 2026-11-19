package supervisor

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/dawnim/dawn-server/support/i18n"
)

type readiness struct {
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

var readyMu sync.Mutex
var readyFile *os.File

func reportReady(state readiness) error {
	readyMu.Lock()
	defer readyMu.Unlock()
	value := os.Getenv("DAWN_READY_FD")
	if value == "" {
		return nil
	}
	if readyFile == nil {
		fd, err := strconv.Atoi(value)
		if err != nil {
			return err
		}
		readyFile = os.NewFile(uintptr(fd), "dawn-ready")
	}
	err := json.NewEncoder(readyFile).Encode(state)
	if state.State == "ready" {
		_ = readyFile.Close()
		readyFile = nil
		_ = os.Unsetenv("DAWN_READY_FD")
	}
	return err
}

// Wait 在依赖尚未就绪时保持进程和控制通道存活，恢复后继续启动。
func Wait(ctx context.Context, check func(context.Context) error) error {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	previous := ""
	for {
		checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := check(checkCtx)
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			return nil
		}
		message := i18n.Message(err, "")
		if message != previous {
			if os.Getenv("DAWN_READY_FD") != "" {
				if e := reportReady(readiness{State: "waiting", Error: message}); e != nil {
					return e
				}
			} else {
				_, _ = os.Stderr.WriteString(i18n.Text("", "等待结构更新") + ": " + message + "\n")
			}
			previous = message
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Pending 报告外部依赖准备中，管理器保留进程并继续接收就绪通知。
func Pending(message string) error {
	return reportReady(readiness{State: "waiting", Error: i18n.Text("", message)})
}
