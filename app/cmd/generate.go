package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"github.com/dawnim/dawn-server/support/codegen"
	"github.com/dawnim/dawn-server/support/i18n"
	"github.com/dawnim/dawn-server/support/supervisor"
	"github.com/prismgo/framework/config"
	"github.com/prismgo/framework/console"
	"github.com/prismgo/framework/database"
	"github.com/prismgo/framework/support"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// GenerateCommand exposes the admin code generator to developers and AI tools.
type GenerateCommand struct{}

func (*GenerateCommand) Definition() *console.Definition {
	d := console.MustDefinition("dawn:generate {table? : 数据表名，省略时列出业务表} {--mode=simple : simple引用模式 full源码模式} {--output= : 输出根目录，默认项目根目录} {--backend=app : 后端目录，相对于输出根目录} {--frontend=admin/src : 前端源码目录，相对于输出根目录} {--directory=business : 前端业务子目录} {--resource= : 资源名称，默认表名转连字符} {--module= : 目标Go模块，默认dawn.module} {--database= : 数据库连接名} {--migration : 生成表迁移快照} {--preview : 只预览，不写入文件} {--json : 以JSON输出}", i18n.Text("", "生成标准CRUD代码"))
	d.Arguments[0].Description = i18n.Text("", d.Arguments[0].Description)
	for i := range d.Options {
		o := &d.Options[i]
		o.Description = i18n.Text("", o.Description)
		if o.Name != "migration" && o.Name != "preview" && o.Name != "json" {
			o.ValueMode = console.OptionValueRequired
			if o.DefaultValue != nil && *o.DefaultValue == "" {
				o.DefaultValue = nil
			}
		}
	}
	return d
}

func (*GenerateCommand) Handle(ctx console.CommandContext) (result error) {
	locale := i18n.Locale(ctx.Context())
	defer func() { result = i18n.Localize(result, locale) }()
	input := ctx.Input()
	display := supervisor.NewDisplay(ctx.IO(), locale, input.OptionBool("json"))
	start := time.Now()
	if !display.JSON {
		display.Heading("生成标准CRUD代码")
		defer func() { display.Result(start, result) }()
	}
	var db *gorm.DB
	if name := input.Option("database"); name != "" {
		var err error
		db, err = database.OpenConnection(name)
		if err != nil {
			return err
		}
		connection, err := db.DB()
		if err != nil {
			return err
		}
		defer connection.Close()
	} else {
		db = database.Resolve()
	}
	db = db.WithContext(ctx.Context()).Session(&gorm.Session{Logger: logger.Discard})
	if input.Argument("table") == "" {
		tables, err := codegen.Tables(db)
		if err != nil {
			return err
		}
		if display.JSON && !display.Options.Quiet && !display.Options.Silent {
			return json.NewEncoder(display.Out).Encode(tables)
		}
		for _, table := range tables {
			_, _ = fmt.Fprintln(display, table)
		}
		return nil
	}
	module := input.Option("module")
	if module == "" {
		module = config.GetString("dawn.module")
	}
	files, err := codegen.Generate(db, codegen.Options{
		Table: input.Argument("table"), Resource: input.Option("resource"),
		Directory: input.Option("directory"), Mode: input.Option("mode"), Module: module,
		Backend: input.Option("backend"), Frontend: input.Option("frontend"), Migration: input.OptionBool("migration"),
	})
	if err != nil {
		return err
	}
	output := input.Option("output")
	if output == "" {
		output = support.BasePath()
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return err
	}
	if !input.OptionBool("preview") {
		if _, err := codegen.Write(output, files); err != nil {
			return err
		}
	}
	if display.JSON && !display.Options.Quiet && !display.Options.Silent {
		sources := map[string]string{}
		for name, data := range files {
			sources[name] = string(data)
		}
		return json.NewEncoder(display.Out).Encode(struct {
			Output  string            `json:"output"`
			Preview bool              `json:"preview"`
			Files   map[string]string `json:"files"`
		}{output, input.OptionBool("preview"), sources})
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		_, _ = fmt.Fprintln(display, filepath.Join(output, name))
	}
	return nil
}
