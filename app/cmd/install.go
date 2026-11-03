package cmd

import (
	"os"

	"github.com/dawnim/dawn-server/support/geoip"
	"github.com/dawnim/dawn-server/support/i18n"
	"github.com/prismgo/framework/config"
	"github.com/prismgo/framework/console"
)

type InstallCommand struct{}

func (*InstallCommand) Definition() *console.Definition {
	return console.MustDefinition("dawn:install {--force : 允许在生产环境执行}", i18n.Text("", "安装Dawn系统表并创建初始管理员"))
}

func (*InstallCommand) Handle(ctx console.CommandContext) (result error) {
	defer func() { result = i18n.Localize(result, i18n.Locale(ctx.Context())) }()
	input := console.CallInput{Options: map[string]any{"force": ctx.Input().OptionBool("force")}}
	for _, command := range []string{"migrate", "db:seed"} {
		if err := ctx.Call(command, input); err != nil {
			return err
		}
	}
	if config.GetBool("dawn.geoip.enabled", true) {
		if _, err := os.Stat(geoip.Path()); os.IsNotExist(err) {
			ctx.IO().Info(i18n.Text("", "正在下载并校验IP地理数据库，请稍候…"))
			if err := geoip.Update(ctx.Context()); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	return nil
}
