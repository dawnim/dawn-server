package cmd

import (
	"github.com/dawnim/dawn-server/support/geoip"
	"github.com/dawnim/dawn-server/support/i18n"
	"github.com/prismgo/framework/console"
)

type GeoIPCommand struct{}

func (*GeoIPCommand) Definition() *console.Definition {
	return console.MustDefinition("dawn:geoip", i18n.Text("", "更新IP地理数据库"))
}
func (*GeoIPCommand) Handle(ctx console.CommandContext) error { return geoip.Update(ctx.Context()) }
