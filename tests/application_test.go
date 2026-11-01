package tests

import (
	"cmp"

	dawn "github.com/dawnim/dawn-server"
	"github.com/dawnim/dawn-server/app/admin"
	"github.com/prismgo/framework/config"
	"github.com/prismgo/framework/encryption"
	"github.com/prismgo/framework/foundation"
)

type testOptions struct {
	DBDriver, DBDSN, Storage, RedisURL, EncryptionKey, Environment string
	LinkSystems                                                    []admin.LinkSystem
}

func newTestApp(o testOptions) (*admin.App, error) {
	o.DBDriver = cmp.Or(o.DBDriver, "sqlite")
	config.Add("dawn", func() map[string]any {
		return map[string]any{"module": "example.com/test", "geoip": map[string]any{"enabled": false}}
	})
	config.Add("app", func() map[string]any {
		return map[string]any{"name": "test", "env": cmp.Or(o.Environment, "testing"), "locale": "zh", "key": "base64:" + o.EncryptionKey,
			"server": map[string]any{"host": "127.0.0.1", "port": config.Env("SERVER_PORT", 5123)}}
	})
	config.Add("database", func() map[string]any {
		return map[string]any{"default": "test", "connections": map[string]any{"test": map[string]any{"driver": o.DBDriver, "dsn": o.DBDSN, "database": o.DBDSN, "max_open_conns": 1}}, "redis": map[string]any{"default": map[string]any{"url": o.RedisURL}}}
	})
	config.Add("filesystem", func() map[string]any {
		return map[string]any{"default": "local", "disks": map[string]any{"local": map[string]any{"driver": "local", "root": o.Storage + "/uploads", "visibility": "private"}}}
	})
	config.Add("logging", func() map[string]any {
		return map[string]any{"default": "stderr", "channels": map[string]any{"stderr": map[string]any{"driver": "stderr", "level": "error"}}}
	})
	app := foundation.Configure(o.Storage).WithExtensionProviders(encryption.ServiceProvider{}, dawn.ServiceProvider{Configure: func(options *admin.Options) { options.Storage = o.Storage; options.LinkSystems = o.LinkSystems }}).WithRouting(func(r *foundation.Routing) { r.Routes(dawn.RegisterRoutes) }).Create()
	if err := app.Boot(); err != nil {
		app.Close()
		return nil, err
	}
	a, err := admin.From(app.Container())
	if err != nil {
		app.Close()
		return nil, err
	}
	return a, nil
}
