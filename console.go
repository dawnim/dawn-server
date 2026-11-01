package dawn

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/dawnim/dawn-server/support/supervisor"
	"github.com/prismgo/framework/console"
	"github.com/prismgo/framework/foundation"
	"github.com/prismgo/framework/kernel"
	prismversion "github.com/prismgo/framework/version"
	"github.com/spf13/cobra"
)

// Version 是当前 Dawn 服务端版本。
const Version = "0.1.0"

func Banner() string { return "Dawn " + Version + " (" + prismversion.Banner() + ")" }

// HandleCommand 使用 Dawn 命令行展示，命令执行与资源释放由应用统一管理。
func HandleCommand(ctx context.Context, app *foundation.Application, argv []string) error {
	if len(argv) == 0 {
		argv = os.Args
	}
	return app.RunContext(func(ctx context.Context) error {
		stopParent, err := supervisor.WatchParent(app.Context(), app.Shutdown)
		if err != nil {
			return err
		}
		defer stopParent()
		supervisor.ConfigureHorizon()
		command, err := newConsoleCommand(app.NewConsoleKernel(), argv)
		if err != nil {
			return err
		}
		command.SetArgs(argv[1:])
		return command.ExecuteContext(ctx)
	}, ctx)
}

func newConsoleCommand(k *kernel.Kernel, argv []string) (*cobra.Command, error) {
	definitions, err := k.All()
	if err != nil {
		return nil, err
	}
	root := &cobra.Command{Use: "Dawn", Short: Banner(), SilenceUsage: true, SilenceErrors: true}
	flags := root.PersistentFlags()
	flags.Bool("ansi", false, "Force ANSI output")
	flags.Bool("no-ansi", false, "Disable ANSI output")
	flags.Bool("no-interaction", false, "Do not ask any interactive question")
	flags.BoolP("quiet", "q", false, "Do not output any message")
	flags.Bool("silent", false, "Do not output any message")
	flags.BoolP("version", "V", false, "Display this application version")
	flags.CountP("verbose", "v", "Increase the verbosity of messages")
	root.RunE = func(cmd *cobra.Command, args []string) error {
		output := consoleOutputOptions(cmd)
		version, _ := cmd.Flags().GetBool("version")
		if version {
			if output.Quiet || output.Silent {
				return nil
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), root.Short)
			return err
		}
		options := console.CommandListOptions{AppName: root.Name(), Output: output}
		switch {
		case cmd == root:
			options.Description = root.Short
		case cmd.Name() == "list":
			if err := cobra.MaximumNArgs(1)(cmd, args); err != nil {
				return err
			}
			if len(args) > 0 {
				options.Namespace = args[0]
			}
			options.Format, _ = cmd.Flags().GetString("format")
			options.Raw, _ = cmd.Flags().GetBool("raw")
			options.Short, _ = cmd.Flags().GetBool("short")
		default:
			return k.RunContextArgv(cmd.Context(), argv)
		}
		return console.RenderCommandList(cmd.OutOrStdout(), definitions, options)
	}
	defaultHelp := root.HelpFunc()
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		output := consoleOutputOptions(cmd)
		if output.Quiet || output.Silent {
			return
		}
		if cmd == root {
			_ = root.RunE(cmd, args)
			return
		}
		defaultHelp(cmd, args)
	})
	for _, definition := range definitions {
		// 补全脚本由当前 Cobra 命令树生成，使用同一品牌和参数定义。
		if definition.Name == "completion" {
			continue
		}
		cmd := &cobra.Command{Use: console.DefinitionUsage(definition), Short: definition.Description,
			Long: definition.Help, Aliases: definition.Aliases, Hidden: definition.Hidden,
			Example: strings.Join(definition.Examples, "\n"), RunE: root.RunE}
		if err := console.BindDefinitionFlags(cmd, definition); err != nil {
			return nil, err
		}
		root.AddCommand(cmd)
	}
	return root, nil
}

func consoleOutputOptions(cmd *cobra.Command) console.OutputOptions {
	ansi, _ := cmd.Flags().GetBool("ansi")
	noANSI, _ := cmd.Flags().GetBool("no-ansi")
	quiet, _ := cmd.Flags().GetBool("quiet")
	silent, _ := cmd.Flags().GetBool("silent")
	return console.ResolveOutputOptions(cmd.OutOrStdout(), ansi, noANSI, quiet, silent)
}
