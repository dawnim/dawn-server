//go:build !windows

package supervisor

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	"github.com/dawnim/dawn-server/support/i18n"
)

func lockManager(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, i18n.Errorf("本项目已有服务管理器运行，请使用控制命令")
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
