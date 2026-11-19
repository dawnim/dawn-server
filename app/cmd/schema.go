package cmd

import (
	"cmp"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dawnim/dawn-server/database/schema"
	"github.com/dawnim/dawn-server/support/i18n"
	"github.com/dawnim/dawn-server/support/supervisor"
	"github.com/prismgo/framework/config"
	"github.com/prismgo/framework/console"
	"github.com/prismgo/framework/database"
	"gorm.io/gorm"
)

// SchemaCommand previews or applies the complete application database definition.
type SchemaCommand struct{}

func (*SchemaCommand) Definition() *console.Definition {
	definition := console.MustDefinition("dawn:schema {action=check : check预览 sync同步} {--database= : 数据库连接名} {--tables= : 只处理指定表，逗号分隔} {--json : 以JSON输出结构差异} {--strict : check发现差异时返回非零状态} {--force : 允许在生产环境执行}", i18n.Text("", "检查或同步完整数据库结构"))
	definition.Arguments[0].Description = i18n.Text("", definition.Arguments[0].Description)
	for i := range definition.Options {
		definition.Options[i].Description = i18n.Text("", definition.Options[i].Description)
		if definition.Options[i].Name == "database" || definition.Options[i].Name == "tables" {
			definition.Options[i].ValueMode = console.OptionValueRequired
			definition.Options[i].DefaultValue = nil
		}
	}
	return definition
}

func (*SchemaCommand) Handle(ctx console.CommandContext) (result error) {
	defer func() { result = i18n.Localize(result, i18n.Locale(ctx.Context())) }()
	action := ctx.Input().Argument("action")
	if action != "check" && action != "sync" {
		return i18n.Errorf("结构操作仅支持check或sync")
	}
	display := supervisor.NewDisplay(ctx.IO(), i18n.Locale(ctx.Context()), ctx.Input().OptionBool("json"))
	start := time.Now()
	if !display.JSON {
		display.Heading("检查或同步完整数据库结构")
		defer func() { display.Result(start, result) }()
	}
	var db *gorm.DB
	if name := ctx.Input().Option("database"); name != "" {
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
	var tables []string
	for _, name := range strings.Split(ctx.Input().Option("tables"), ",") {
		if name = strings.TrimSpace(name); name != "" {
			tables = append(tables, name)
		}
	}
	definition := schema.Resolve()
	plan, err := definition.Check(ctx.Context(), db, tables...)
	if err != nil {
		return err
	}
	if !display.JSON && len(plan.Changes) > 0 {
		_, _ = fmt.Fprintln(display, i18n.Text(display.Locale, "结构差异预览（%d项）", len(plan.Changes)))
		labels := map[string]string{"create_table": "新增表", "drop_table": "删除表", "add_column": "新增列", "alter_column": "修改列", "drop_column": "删除列", "add_index": "新增索引", "drop_index": "删除索引", "add_constraint": "新增约束", "drop_constraint": "删除约束", "table_comment": "表注释"}
		for _, c := range plan.Changes {
			name := c.Table
			if c.Name != "" {
				name += "." + c.Name
			}
			risk := "低"
			if c.Risk {
				risk = "注意"
			}
			lines := []string{
				i18n.Text(display.Locale, "对 %s %s 风险等级:%s", name, i18n.Text(display.Locale, labels[c.Action]), i18n.Text(display.Locale, risk)),
				i18n.Text(display.Locale, "当前结构"), cmp.Or(c.Before, "-"),
				i18n.Text(display.Locale, "目标结构"), cmp.Or(c.After, "-"),
			}
			if display.Options.ANSI {
				if c.Risk {
					lines[0] = "\x1b[31m" + lines[0] + "\x1b[0m"
				}
				lines[1] = "\x1b[32m" + lines[1] + "\x1b[0m"
				lines[3] = "\x1b[33m" + lines[3] + "\x1b[0m"
			}
			_, _ = fmt.Fprintf(display, "\n%s\n", strings.Join(lines, "\n"))
		}
		_, _ = fmt.Fprintln(display)
	}
	if action == "sync" && len(plan.Changes) > 0 {
		if config.GetString("app.env") == "production" && !ctx.Input().OptionBool("force") {
			return ctx.Fail(i18n.Text(display.Locale, "生产环境同步结构需要--force"))
		}
		_, _ = fmt.Fprintln(display, i18n.Text(display.Locale, "正在同步数据库结构"))
		if err = definition.Apply(ctx.Context(), db, plan); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(display, i18n.Text(display.Locale, "正在复查数据库结构"))
		remaining, err := definition.Check(ctx.Context(), db, tables...)
		if err != nil {
			return err
		}
		if len(remaining.Changes) > 0 {
			return i18n.Errorf("结构同步后仍有%d项差异，请重新执行check", len(remaining.Changes))
		}
	}
	if display.JSON && !display.Options.Quiet && !display.Options.Silent {
		if err = json.NewEncoder(display.Out).Encode(struct {
			Action string `json:"action"`
			*schema.Plan
			InSync bool `json:"in_sync"`
		}{action, plan, action == "sync" || len(plan.Changes) == 0}); err != nil {
			return err
		}
	}
	if action == "check" && len(plan.Changes) > 0 {
		if ctx.Input().OptionBool("strict") {
			return ctx.Fail(i18n.Text(display.Locale, "数据库结构有%d项差异", len(plan.Changes)))
		}
		if !display.JSON {
			_, _ = fmt.Fprintln(display, i18n.Text(display.Locale, "检查完成，发现%d项差异", len(plan.Changes)))
		}
		return nil
	}
	if !display.JSON {
		_, _ = fmt.Fprintln(display, i18n.Text(display.Locale, "数据库结构已一致"))
	}
	return nil
}
