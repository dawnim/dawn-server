//go:build !windows

package supervisor

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

var errManagerRunning = errors.New("本项目已有服务管理器运行，请使用控制命令")

func lockManager(path string) (*os.File, error) {
	if value := os.Getenv("DAWN_MANAGER_LOCK_FD"); value != "" {
		_ = os.Unsetenv("DAWN_MANAGER_LOCK_FD")
		fd, err := strconv.Atoi(value)
		if err != nil {
			return nil, err
		}
		closeOnExec(fd)
		return os.NewFile(uintptr(fd), path), nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errManagerRunning
		}
		return nil, err
	}
	return file, nil
}

func configureProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

func closeOnExec(fd int) { syscall.CloseOnExec(fd) }

func terminateProcess(pid int, force bool) error {
	if force {
		return syscall.Kill(-pid, syscall.SIGKILL)
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}

func socketAbsent(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED)
}
