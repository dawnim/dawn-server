package tests

import (
	"cmp"
	"context"
	"path/filepath"

	dawn "github.com/dawnim/dawn-server"
	"github.com/dawnim/dawn-server/app/admin"
	fixtures "github.com/dawnim/dawn-server/tests/fixtures/database/migrations"
	_ "github.com/dawnim/dawn-server/tests/fixtures/database/seeders"
	"github.com/prismgo/framework/config"
	"github.com/prismgo/framework/console"
	"github.com/prismgo/framework/encryption"
	"github.com/prismgo/framework/foundation"
	"github.com/prismgo/framework/kernel"
)

type testOptions struct {
	DBDriver, DBDSN, Storage, RedisURL, EncryptionKey, Environment string
	LinkSystems                                                    []admin.LinkSystem
	AdminUsername, AdminPassword                                   string
	MigrationPaths                                                 []string
}

func newTestApp(o testOptions) (*admin.App, error) {
	o.DBDriver = cmp.Or(o.DBDriver, "sqlite")
	config.Add("dawn", func() map[string]any {
		return map[string]any{"module": "example.com/test", "admin_username": cmp.Or(o.AdminUsername, "admin"), "admin_password": cmp.Or(o.AdminPassword, "123456"), "geoip": map[string]any{"enabled": false}}
	})
	config.Add("app", func() map[string]any {
		return map[string]any{"name": "test", "env": cmp.Or(o.Environment, "testing"), "locale": "zh", "key": "base64:" + o.EncryptionKey,
			"server": map[string]any{"host": "127.0.0.1", "port": config.Env("SERVER_PORT", 5123)}}
	})
	config.Add("database", func() map[string]any {
		return map[string]any{"default": "test", "connections": map[string]any{"test": map[string]any{"driver": o.DBDriver, "dsn": o.DBDSN, "database": o.DBDSN, "max_open_conns": 1}}, "redis": map[string]any{"default": map[string]any{"url": o.RedisURL}}}
	})
	config.Add("horizon", func() map[string]any { return map[string]any{"path": "/admin/horizon"} })
	config.Add("filesystem", func() map[string]any {
		return map[string]any{"default": "local", "disks": map[string]any{"local": map[string]any{"driver": "local", "root": o.Storage + "/uploads", "visibility": "private"}, "public": map[string]any{"driver": "local", "root": o.Storage + "/app/public", "url": "/storage", "visibility": "public"}}}
	})
	config.Add("logging", func() map[string]any {
		return map[string]any{"default": "stderr", "channels": map[string]any{"stderr": map[string]any{"driver": "stderr", "level": "error"}}}
	})
	app := foundation.Configure(o.Storage).WithExtensionProviders(encryption.ServiceProvider{}, dawn.ServiceProvider{Configure: func(options *admin.Options) { options.Storage = o.Storage; options.LinkSystems = o.LinkSystems }}).WithRouting(func(r *foundation.Routing) {
		r.Routes(dawn.RegisterRoutes)
		r.MigrationPaths(append([]string{fixtures.Directory}, o.MigrationPaths...)...)
		r.SeedPaths(filepath.Join(fixtures.Directory, "..", "seeders"))
	}).Create()
	if err := app.Boot(); err != nil {
		app.Close()
		return nil, err
	}
	app.NewConsoleKernel()
	a, err := admin.From(app.Container())
	if err != nil {
		app.Close()
		return nil, err
	}
	return a, nil
}

// 通过应用已绑定的原生 Kernel 验证数据库命令。
func databaseCommand(a *admin.App, command string, input ...console.CallInput) error {
	app := a.Runtime.(*foundation.Application)
	raw, err := app.Container().Make(kernel.ArtisanFacadeKey)
	if err != nil {
		return err
	}
	return raw.(*kernel.Kernel).Call(context.Background(), command, input...)
}
