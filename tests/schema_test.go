package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/dawnim/dawn-server/app/cmd"
	"github.com/dawnim/dawn-server/database/migrations"
	"github.com/dawnim/dawn-server/database/schema"
	"github.com/dawnim/dawn-server/support/i18n"
	"github.com/prismgo/framework/console"
	"github.com/spf13/cobra"
)

type schemaUser struct {
	migrations.User
	Nickname     string `gorm:"type:varchar(300);not null;default:''"`
	CustomerCode string `gorm:"not null;default:'';uniqueIndex"`
}
type schemaOrder struct {
	ID    uint   `gorm:"primaryKey"`
	Title string `gorm:"not null;size:200"`
}

func TestSchemaCommands(t *testing.T) {
	store := miniredis.RunT(t)
	a, err := newTestApp(testOptions{DBDSN: ":memory:", Storage: t.TempDir(), RedisURL: "redis://" + store.Addr(), Schema: func(d *schema.Definition) {
		d.Tables["users"] = schema.Table{Model: &schemaUser{}, Comment: "系统:普通用户表"}
		d.Tables["orders"] = schema.Table{Model: &schemaOrder{}, Comment: "业务订单表"}
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, command := range []string{"migrate", "db:seed"} {
		if err = databaseCommand(a, command); err != nil {
			t.Fatal(err)
		}
	}
	if err = databaseCommand(a, "dawn:schema check --json"); err != nil {
		t.Fatal("check should complete after reporting differences", err)
	}
	if err = databaseCommand(a, "dawn:schema check --json --strict"); err == nil || !strings.Contains(err.Error(), "差异") {
		t.Fatal("strict check must report differences", err)
	}
	// Exercise real Console rendering, including paragraph colors, plain text and JSON.
	preview := func(locale string, jsonOutput, ansi bool) (string, error) {
		command := &cmd.SchemaCommand{}
		definition := command.Definition()
		cobraCommand := &cobra.Command{}
		if err := console.BindDefinitionFlags(cobraCommand, *definition); err != nil {
			t.Fatal(err)
		}
		if jsonOutput {
			cobraCommand.Flags().Set("json", "true")
		}
		var output bytes.Buffer
		input := console.NewInput(*definition, cobraCommand, []string{"check"})
		context := console.NewCommandContext(i18n.WithLocale(context.Background(), locale), command, *definition, input, console.NewIOWithOutputOptions(nil, &output, &output, console.OutputOptions{ANSI: ansi}), nil, cobraCommand)
		err := command.Handle(context)
		return output.String(), err
	}
	text, previewErr := preview("en", false, true)
	if previewErr != nil || !strings.Contains(text, "Risk level: Attention") || !strings.Contains(text, "\x1b[31mOn ") || !strings.Contains(text, "\x1b[32mCurrent schema\x1b[0m\n") || !strings.Contains(text, "\x1b[33mTarget schema\x1b[0m\n") || !strings.Contains(text, "Check completed") {
		t.Fatal("preview rendering", text, previewErr)
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "varchar") && strings.Contains(line, "\x1b[") {
			t.Fatal("schema content should use the default color", line)
		}
	}
	text, previewErr = preview("zh", false, false)
	if previewErr != nil || !strings.Contains(text, "\n对 orders 新增表 风险等级:低\n当前结构\n-\n目标结构\n业务订单表\n\n") || !strings.Contains(text, "检查完成，发现") || strings.Contains(text, "\x1b[") {
		t.Fatal("plain text paragraphs", text, previewErr)
	}
	text, previewErr = preview("zh", true, true)
	var report struct {
		Changes []schema.Change `json:"changes"`
		InSync  bool            `json:"in_sync"`
	}
	if err := json.Unmarshal([]byte(text), &report); err != nil || previewErr != nil || report.InSync || len(report.Changes) == 0 {
		t.Fatal("JSON preview", text, previewErr, err)
	}
	if a.DB.Migrator().HasTable("orders") {
		t.Fatal("check performed DDL")
	}
	if err = a.DB.Exec("CREATE TABLE obsolete_business (id INTEGER PRIMARY KEY)").Error; err != nil {
		t.Fatal(err)
	}
	if err = databaseCommand(a, "dawn:schema sync --json"); err != nil {
		t.Fatal(err)
	}
	if err = databaseCommand(a, "dawn:schema check --json"); err != nil {
		t.Fatal(err)
	}
	if !a.DB.Migrator().HasTable("orders") || !a.DB.Migrator().HasColumn("users", "customer_code") || a.DB.Migrator().HasTable("obsolete_business") {
		t.Fatal("business tables/default override not synchronized")
	}
	var count int64
	if err = a.DB.Table("migrations").Count(&count).Error; err != nil || count != 1 {
		t.Fatal("sync rewrote migration history", count, err)
	}
	var settings int64
	if err = a.DB.Table("admin_settings").Count(&settings).Error; err != nil || settings != 4 {
		t.Fatal("sync changed settings", settings, err)
	}
	if err = a.DB.Migrator().DropColumn(&migrations.SupportSession{}, "offline_since"); err != nil {
		t.Fatal(err)
	}
	if err = migrations.CheckDefinition(context.Background(), a.DB, schema.Resolve()); err == nil {
		t.Fatal("startup readiness missed required field")
	}
	if err = databaseCommand(a, "dawn:schema sync --tables=support_sessions"); err != nil {
		t.Fatal(err)
	}
	if err = migrations.CheckDefinition(context.Background(), a.DB, schema.Resolve()); err != nil {
		t.Fatal(err)
	}
	if err = databaseCommand(a, "dawn:schema sync --json"); err != nil {
		t.Fatal("second sync not idempotent", err)
	}
}

func TestSchemaProductionForce(t *testing.T) {
	store := miniredis.RunT(t)
	a, err := newTestApp(testOptions{DBDSN: ":memory:", Storage: t.TempDir(), RedisURL: "redis://" + store.Addr(), Environment: "production"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err = databaseCommand(a, "migrate", console.CallInput{Options: map[string]any{"force": true}}); err != nil {
		t.Fatal(err)
	}
	if err = a.DB.Exec("CREATE TABLE obsolete (id INTEGER PRIMARY KEY)").Error; err != nil {
		t.Fatal(err)
	}
	if err = databaseCommand(a, "dawn:schema sync --json"); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatal(err)
	}
	if !a.DB.Migrator().HasTable("obsolete") {
		t.Fatal("production sync executed without force")
	}
	if err = databaseCommand(a, "dawn:schema sync --json --force"); err != nil {
		t.Fatal(err)
	}
}
