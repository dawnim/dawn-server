package supervisor

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/prismgo/framework/logger"
)

// Watch 监听应用及本地模块源码，编译成功后更新运行中的 Go 服务。
func Watch(ctx context.Context, directory, runtime string, reload func(string) error) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()
	build, err := os.MkdirTemp(runtime, "build-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(build)
	binary := filepath.Join(build, "app")
	skipped := map[string]bool{"node_modules": true, "vendor": true, "docs": true, "public": true, "storage": true, "bin": true, "dist": true}
	add := func(root string) error {
		return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() {
				return nil
			}
			if path != root && (skipped[entry.Name()] || strings.HasPrefix(entry.Name(), ".")) {
				return filepath.SkipDir
			}
			return watcher.Add(path)
		})
	}
	refresh := func() error {
		modules := exec.CommandContext(ctx, "go", "list", "-m", "-f", "{{if .Main}}{{.Dir}}{{else if .Replace}}{{if not .Replace.Version}}{{.Replace.Dir}}{{end}}{{end}}", "all")
		modules.Dir, modules.Stderr = directory, os.Stderr
		roots, err := modules.Output()
		if err != nil {
			return err
		}
		for root := range strings.SplitSeq(strings.TrimSpace(string(roots)), "\n") {
			if root != "" {
				if err := add(root); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := refresh(); err != nil {
		return err
	}
	logger.Info("Dawn local watch", map[string]any{"directory": directory})
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-watcher.Errors:
			return err
		case event := <-watcher.Events:
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}
			name := filepath.Base(event.Name)
			if skipped[name] || strings.HasPrefix(name, ".") {
				continue
			}
			if entry, err := os.Stat(event.Name); err == nil && entry.IsDir() {
				if err := add(event.Name); err != nil {
					return err
				}
				continue
			}
			if (filepath.Ext(name) == ".go" && !strings.HasSuffix(name, "_test.go")) || name == "go.mod" || name == "go.sum" || name == "go.work" || name == "go.work.sum" {
				timer.Reset(400 * time.Millisecond)
			}
		case <-timer.C:
			command := exec.CommandContext(ctx, "go", "build", "-o", binary+".next", ".")
			command.Dir, command.Stdout, command.Stderr = directory, os.Stdout, os.Stderr
			configureProcess(command)
			command.Cancel = func() error { return terminateProcess(command.Process.Pid, true) }
			if err := command.Run(); err != nil {
				if ctx.Err() == nil {
					logger.Error("Dawn local build", map[string]any{"error": err})
				}
				continue
			}
			if err := os.Rename(binary+".next", binary); err != nil {
				return err
			}
			if err := refresh(); err != nil {
				logger.Error("Dawn local watch", map[string]any{"error": err})
			}
			if err := reload(binary); err != nil {
				logger.Error("Dawn local reload", map[string]any{"error": err})
			} else {
				logger.Info("Dawn local reloaded", map[string]any{"executable": binary})
			}
		}
	}
}
