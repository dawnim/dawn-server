package cmd

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dawnim/dawn-server/support/i18n"
	"github.com/dawnim/dawn-server/support/supervisor"
	"github.com/prismgo/framework/console"
	"github.com/prismgo/framework/support"
)

// RenameCommand updates a newly cloned application's module and project name.
type RenameCommand struct{}

func (*RenameCommand) Definition() *console.Definition {
	d := console.MustDefinition("dawn:rename {module : 新Go模块路径，例如example.com/my-app}", i18n.Text("", "修改项目名称与Go模块"))
	d.Arguments[0].Description = i18n.Text("", d.Arguments[0].Description)
	return d
}

func (*RenameCommand) Handle(ctx console.CommandContext) (result error) {
	locale := i18n.Locale(ctx.Context())
	defer func() { result = i18n.Localize(result, locale) }()
	display := supervisor.NewDisplay(ctx.IO(), locale, false)
	display.Heading("修改项目名称与Go模块")
	start := time.Now()
	defer func() { display.Result(start, result) }()

	root, module := support.BasePath(), ctx.Input().Argument("module")
	command := exec.CommandContext(ctx.Context(), "go", "mod", "edit", "-json")
	command.Dir, command.Stderr = root, display
	data, err := command.Output()
	if err != nil {
		return err
	}
	var current struct{ Module struct{ Path string } }
	if err = json.Unmarshal(data, &current); err != nil {
		return err
	}
	command = exec.CommandContext(ctx.Context(), "go", "mod", "edit", "-module="+module)
	command.Dir, command.Stderr = root, display
	if err = command.Run(); err != nil {
		return err
	}
	oldModule := current.Module.Path
	oldName, name := path.Base(oldModule), path.Base(module)
	goNames := strings.NewReplacer(
		strconv.Quote(oldModule), strconv.Quote(module),
		`"`+oldModule+`/`, `"`+module+`/`,
		strconv.Quote(oldName), strconv.Quote(name),
	)
	packageName := regexp.MustCompile(`(?m)^([ \t]*"name"[ \t]*:[ \t]*)"[^"]*"`)
	envEntries := regexp.MustCompile(`(?m)^[ \t]*(?:#[ \t]*)?(?:APP_NAME|CACHE_PREFIX|SESSION_PREFIX|QUEUE_FAILED_PREFIX|QUEUE_BATCHING_PREFIX|QUEUE_RESTART_KEY|REDIS_QUEUE_PREFIX|HORIZON_PREFIX)[ \t]*=[^\r\n]*`)
	err = filepath.WalkDir(root, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if file != root {
				switch entry.Name() {
				case ".git", ".pnpm-store", ".venv", "node_modules", "vendor", "storage", "public", "docs", "bin", "dist":
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(root, file)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		isEnv := relative == ".env" || relative == ".env.example"
		isPackage := relative == "admin/package.json"
		isSupervisor := filepath.Dir(relative) == "." && strings.HasPrefix(relative, "supervisord") && strings.HasSuffix(relative, ".conf")
		if filepath.Ext(file) != ".go" && !isEnv && !isPackage && !isSupervisor {
			return nil
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		before, after := string(data), string(data)
		switch {
		case isEnv:
			after = envEntries.ReplaceAllStringFunc(before, func(line string) string {
				key, value, _ := strings.Cut(line, "=")
				if strings.HasSuffix(strings.TrimSpace(key), "APP_NAME") {
					value = name
				} else {
					value = strings.Replace(value, oldName+":", name+":", 1)
				}
				return key + "=" + value
			})
		case isPackage:
			after = packageName.ReplaceAllString(before, "${1}"+strconv.Quote(name+"-admin"))
		case isSupervisor:
			after = strings.ReplaceAll(before, oldName, name)
		default:
			after = goNames.Replace(before)
		}
		if after == before {
			return nil
		}
		if err := os.WriteFile(file, []byte(after), 0644); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(display, relative)
		return nil
	})
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(display, i18n.Text(locale, "项目已更新为%s（%s）", name, module))
	return nil
}
