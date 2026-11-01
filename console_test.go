package dawn

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/prismgo/framework/console"
	"github.com/prismgo/framework/foundation"
	"github.com/prismgo/framework/kernel"
)

func TestConsole(t *testing.T) {
	for _, test := range []struct {
		name, want, failure string
		args                []string
		run                 bool
	}{
		{name: "root", want: Banner()},
		{name: "help", args: []string{"--help"}, want: Banner()},
		{name: "help_command", args: []string{"help", "probe"}, want: "Dawn probe <name>"},
		{name: "command_help", args: []string{"probe", "--help"}, want: "Dawn probe <name>"},
		{name: "version", args: []string{"--version"}, want: Banner() + "\n"},
		{name: "command_version", args: []string{"probe", "-V"}, want: Banner() + "\n"},
		{name: "quiet", args: []string{"-qV"}},
		{name: "silent", args: []string{"probe", "--help", "--silent"}},
		{name: "markdown", args: []string{"list", "--format=md"}, want: "Dawn"},
		{name: "json", args: []string{"list", "--format=json"}, want: `"name": "probe"`},
		{name: "raw", args: []string{"ls", "--raw"}, want: "probe"},
		{name: "namespace", args: []string{"list", "probe"}, want: "probe"},
		{name: "unknown_command", args: []string{"missing"}, failure: "unknown command"},
		{name: "unknown_option", args: []string{"probe", "--missing"}, failure: "unknown flag"},
		{name: "arguments", args: []string{"ls", "one", "two"}, failure: "accepts at most"},
		{name: "value_looks_like_flag", args: []string{"p", "admin", "--value=--version"}, run: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := foundation.Configure(t.TempDir()).Create()
			if err := app.Boot(); err != nil {
				t.Fatal(err)
			}
			defer app.Close()
			k := app.NewConsoleKernel()
			called := false
			definition := console.MustDefinition("probe {name} {--value=}", "Probe command")
			definition.Aliases = []string{"p"}
			if err := k.Starting(func(k *kernel.Kernel) error {
				k.RegisterClosure(*console.MustDefinition("probe:child", "Namespaced command"), func(console.CommandContext) error { return nil })
				k.RegisterClosure(*definition, func(ctx console.CommandContext) error {
					called = true
					if ctx.Input().Argument("name") != "admin" || ctx.Input().Option("value") != "--version" {
						t.Fatal("命令参数未原样传递")
					}
					return nil
				})
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			argv := append([]string{"server"}, test.args...)
			cmd, err := newConsoleCommand(k, argv)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(test.args)
			err = cmd.ExecuteContext(context.Background())
			if test.failure != "" {
				if err == nil || !strings.Contains(err.Error(), test.failure) {
					t.Fatalf("错误: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if called != test.run || !strings.Contains(out.String(), test.want) {
				t.Fatalf("执行=%v 输出=%s", called, &out)
			}
			if test.want == "" && out.Len() != 0 {
				t.Fatalf("应静默输出: %s", &out)
			}
			if test.name == "json" {
				var result struct {
					Commands []console.CommandDescriptor `json:"commands"`
				}
				if err := json.Unmarshal(out.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				found := slices.ContainsFunc(result.Commands, func(c console.CommandDescriptor) bool {
					return c.Name == "probe" && slices.Equal(c.Aliases, []string{"p"})
				})
				if !found {
					t.Fatal("命令别名丢失")
				}
			}
		})
	}
}
