package cmd

import (
	"os"

	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/database/migrations"
	"github.com/dawnim/dawn-server/support/geoip"
	"github.com/dawnim/dawn-server/support/i18n"
	"github.com/prismgo/framework/config"
	"github.com/prismgo/framework/console"
)

type InstallCommand struct{}

func (*InstallCommand) Definition() *console.Definition {
	return console.MustDefinition(i18n.Text("", "dawn:install {username=admin :管理员用户名}"), i18n.Text("", "安装Dawn系统表并创建初始管理员"))
}

func (*InstallCommand) Handle(ctx console.CommandContext) (result error) {
	defer func() { result = i18n.Localize(result, i18n.Locale(ctx.Context())) }()
	a := admin.Resolve()
	if err := migrations.Run(ctx.Context(), a); err != nil {
		return err
	}
	if err := ctx.Call("migrate"); err != nil {
		return err
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
	password := config.GetString("dawn.admin_password", admin.DefaultAdminPassword)
	return a.InstallDefaults(ctx.Context(), ctx.Input().Argument("username"), password)
}
