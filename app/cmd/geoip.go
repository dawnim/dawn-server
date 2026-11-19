package cmd

import (
	"time"

	"github.com/dawnim/dawn-server/support/geoip"
	"github.com/dawnim/dawn-server/support/i18n"
	"github.com/dawnim/dawn-server/support/supervisor"
	"github.com/prismgo/framework/console"
)

type GeoIPCommand struct{}

func (*GeoIPCommand) Definition() *console.Definition {
	return console.MustDefinition("dawn:geoip", i18n.Text("", "更新IP地理数据库"))
}
func (*GeoIPCommand) Handle(ctx console.CommandContext) (err error) {
	display := supervisor.NewDisplay(ctx.IO(), i18n.Locale(ctx.Context()), false)
	display.Heading("更新IP地理数据库")
	start := time.Now()
	defer func() { display.Result(start, err) }()
	return geoip.Update(ctx.Context())
}
