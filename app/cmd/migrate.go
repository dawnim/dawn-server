package cmd

import (
	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/database/migrations"
	"github.com/dawnim/dawn-server/support/i18n"
	"github.com/prismgo/framework/console"
)

type MigrateCommand struct{}

func (*MigrateCommand) Definition() *console.Definition {
	return console.MustDefinition("dawn:migrate", i18n.Text("", "更新后台系统表"))
}
func (*MigrateCommand) Handle(ctx console.CommandContext) (result error) {
	defer func() { result = i18n.Localize(result, i18n.Locale(ctx.Context())) }()
	return migrations.Run(ctx.Context(), admin.Resolve())
}
