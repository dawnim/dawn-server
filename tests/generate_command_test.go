package tests

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/app/cmd"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/support/codegen"
	"github.com/prismgo/framework/console"
	"github.com/spf13/cobra"
)

func TestGenerateCommand(t *testing.T) {
	store := miniredis.RunT(t)
	a, err := newTestApp(testOptions{DBDSN: ":memory:", Storage: t.TempDir(), RedisURL: "redis://" + store.Addr()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := databaseCommand(a, "migrate"); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.AutoMigrate(&generatedItem{}); err != nil {
		t.Fatal(err)
	}
	// A department-only schema has no creator column to use for self scope.
	actor := &admin.Actor{User: models.AdminUser{Base: models.Base{ID: 1}, DepartmentID: 2}}
	actor.User.Roles = []models.Role{{Status: models.Enabled, DataScope: models.ScopeDepartment}}
	if err := a.DB.Exec("CREATE TABLE department_records (id INTEGER PRIMARY KEY, department_id BIGINT NOT NULL)").Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Exec("INSERT INTO department_records VALUES (1,2),(2,3)").Error; err != nil {
		t.Fatal(err)
	}
	var total int64
	if err := a.DB.Table("department_records").Scopes(actor.Scope("", "department_id")).Count(&total).Error; err != nil || total != 1 {
		t.Fatal("department-only scope", total, err)
	}
	if err := a.DB.Migrator().DropTable("department_records"); err != nil {
		t.Fatal(err)
	}
	// Use the registered Console command as well as its captured JSON output.
	if err := databaseCommand(a, "dawn:generate app_example_items --preview"); err != nil {
		t.Fatal(err)
	}
	run := func(table string, options map[string]string) (string, error) {
		t.Helper()
		command := &cmd.GenerateCommand{}
		definition := command.Definition()
		cobraCommand := &cobra.Command{}
		if err := console.BindDefinitionFlags(cobraCommand, *definition); err != nil {
			t.Fatal(err)
		}
		cobraCommand.Flags().Set("json", "true")
		for key, value := range options {
			if err := cobraCommand.Flags().Set(key, value); err != nil {
				t.Fatal(err)
			}
		}
		args := []string{}
		if table != "" {
			args = append(args, table)
		}
		var output bytes.Buffer
		input := console.NewInput(*definition, cobraCommand, args)
		ctx := console.NewCommandContext(context.Background(), command, *definition, input, console.NewIOWithOutputOptions(nil, &output, &output, console.OutputOptions{}), nil, cobraCommand)
		err := command.Handle(ctx)
		return output.String(), err
	}
	output, err := run("", nil)
	var tables []string
	if err != nil || json.Unmarshal([]byte(output), &tables) != nil || !reflect.DeepEqual(tables, []string{"app_example_items"}) {
		t.Fatal("CLI table listing", output, err)
	}
	for _, mode := range []string{"simple", "full"} {
		t.Run(mode, func(t *testing.T) {
			outputRoot := filepath.Join(t.TempDir(), "custom output")
			options := map[string]string{"mode": mode, "output": outputRoot, "preview": "true"}
			output, err := run("app_example_items", options)
			var result struct {
				Output  string            `json:"output"`
				Preview bool              `json:"preview"`
				Files   map[string]string `json:"files"`
			}
			if err != nil || json.Unmarshal([]byte(output), &result) != nil || !result.Preview || result.Output != outputRoot {
				t.Fatal("CLI JSON preview", output, err)
			}
			if _, err := os.Stat(outputRoot); !os.IsNotExist(err) {
				t.Fatal("preview wrote output", err)
			}
			want, err := codegen.Generate(a.DB, codegen.Options{Table: "app_example_items", Mode: mode, Module: "example.com/test"})
			if err != nil || len(want) != len(result.Files) {
				t.Fatal("CLI and shared generator disagree", err)
			}
			for name, code := range want {
				if string(code) != result.Files[name] {
					t.Fatal("CLI source mismatch", name)
				}
			}
			options["preview"], options["module"], options["backend"], options["frontend"], options["directory"] = "false", "example.com/extension", "domain", "src", "crm"
			if output, err := run("app_example_items", options); err != nil {
				t.Fatal("CLI write", output, err)
			}
			path := filepath.Join(outputRoot, "domain/admin/controllers/app_example_items.go")
			controller, err := os.ReadFile(path)
			if err != nil || !strings.Contains(string(controller), "example.com/extension/domain/models") {
				t.Fatal("custom module/backend", string(controller), err)
			}
			if _, err := os.Stat(filepath.Join(outputRoot, "src/views/crm/app_example_items/index.vue")); err != nil {
				t.Fatal("custom frontend directory", err)
			}
			if err := os.WriteFile(path, []byte("custom code"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := run("app_example_items", options); err == nil {
				t.Fatal("existing source overwritten")
			}
			if data, _ := os.ReadFile(path); string(data) != "custom code" {
				t.Fatal("existing source changed")
			}
		})
	}
}
