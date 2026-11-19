package cmd

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/dawnim/dawn-server/support/i18n"
	"github.com/dawnim/dawn-server/support/supervisor"
	embeddingtools "github.com/dawnim/dawn-server/tools/embedding"
	"github.com/prismgo/framework/config"
	"github.com/prismgo/framework/console"
	"github.com/prismgo/framework/support"
)

// EmbeddingCommand 显式安装项目内的向量工具和Python依赖，普通启动不安装依赖。
type EmbeddingCommand struct{}

func (*EmbeddingCommand) Definition() *console.Definition {
	return console.MustDefinition("dawn:embedding {action=install : install}", i18n.Text("", "安装本地向量服务环境"))
}

func (*EmbeddingCommand) Handle(ctx console.CommandContext) (result error) {
	defer func() { result = i18n.Localize(result, i18n.Locale(ctx.Context())) }()
	display := supervisor.NewDisplay(ctx.IO(), i18n.Locale(ctx.Context()), false)
	display.Heading("安装本地向量服务环境")
	started := time.Now()
	defer func() { display.Result(started, result) }()
	if ctx.Input().Argument("action") != "install" {
		return i18n.Errorf("未知向量服务操作")
	}
	directory, python := embeddingPaths()
	if err := embeddingtools.Publish(directory); err != nil {
		return err
	}
	if _, err := os.Stat(python); errors.Is(err, os.ErrNotExist) {
		command := exec.CommandContext(ctx.Context(), config.GetString("dawn.embedding.python", "python3"), "-m", "venv", filepath.Join(directory, ".venv"))
		command.Stdout, command.Stderr = os.Stdout, os.Stderr
		if err = command.Run(); err != nil {
			return err
		}
	}
	command := exec.CommandContext(ctx.Context(), python, "-m", "pip", "install", "-r", filepath.Join(directory, "requirements.txt"))
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	return command.Run()
}

func embeddingPaths() (string, string) {
	directory := support.BasePath(config.GetString("dawn.embedding.directory", "storage/support/embedding"))
	folder, name := "bin", "python"
	if runtime.GOOS == "windows" {
		folder, name = "Scripts", "python.exe"
	}
	return directory, filepath.Join(directory, ".venv", folder, name)
}

// runEmbedding 复用管理器的进程组和父进程监听；模型加载期间保留等待状态。
func runEmbedding(ctx context.Context) error {
	directory, python := embeddingPaths()
	if _, err := os.Stat(python); err != nil {
		return i18n.Errorf("请先执行dawn:embedding install安装向量环境")
	}
	if err := embeddingtools.Publish(directory); err != nil {
		return err
	}
	host := config.GetString("dawn.embedding.host", "127.0.0.1")
	port := strconv.Itoa(config.GetInt("dawn.embedding.port", 8852))
	probeHost := host
	if host == "0.0.0.0" || host == "::" {
		probeHost = "127.0.0.1"
	}
	endpoint := net.JoinHostPort(probeHost, port)
	if conn, err := net.DialTimeout("tcp", endpoint, 100*time.Millisecond); err == nil {
		_ = conn.Close()
		return i18n.Errorf("向量服务端口已被占用: %s", endpoint)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	command := exec.CommandContext(runCtx, python, "-m", "uvicorn", "main:app", "--host", host, "--port", port, "--workers", "1")
	command.Dir, command.Stdout, command.Stderr = directory, os.Stdout, os.Stderr
	command.Env = append(os.Environ(),
		"HF_HOME="+filepath.Join(directory, "models"),
		"EMBEDDING_MODEL="+config.GetString("dawn.embedding.model", "Qwen/Qwen3-Embedding-0.6B"),
		"EMBEDDING_THREADS="+strconv.Itoa(config.GetInt("dawn.embedding.threads", 4)),
		"EMBEDDING_MAX_LENGTH="+strconv.Itoa(config.GetInt("dawn.embedding.max_length", 2048)),
		"EMBEDDING_DEVICE="+config.GetString("dawn.embedding.device", "cpu"))
	command.Cancel = func() error {
		if err := command.Process.Signal(os.Interrupt); err != nil {
			return command.Process.Kill()
		}
		return nil
	}
	command.WaitDelay = 10 * time.Second
	if err := command.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	finished := false
	defer func() {
		cancel()
		if !finished {
			<-done
		}
	}()
	// 保持进程可停止，不让首次下载和加载模型触发管理器30秒启动超时。
	if err := supervisor.Pending("正在加载向量模型，首次启动需要下载权重"); err != nil {
		return err
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	client := &http.Client{Timeout: time.Second}
	ready := false
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-done:
			finished = true
			if err == nil {
				return i18n.Errorf("向量服务意外退出")
			}
			return err
		case <-ticker.C:
			if ready {
				continue
			}
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+endpoint+"/health", nil)
			response, err := client.Do(req)
			if err != nil {
				continue
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode != http.StatusOK {
				continue
			}
			if err = supervisor.Ready(); err != nil {
				return err
			}
			ready = true
		}
	}
}
