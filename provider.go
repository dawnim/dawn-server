// Package dawn 将后台服务接入应用容器与生命周期。
package dawn

import (
	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/app/providers"
	"github.com/dawnim/dawn-server/routes"
	"github.com/dawnim/dawn-server/support/storage"
	"github.com/gin-gonic/gin"
	pc "github.com/prismgo/framework/contracts/provider"
	"github.com/prismgo/framework/database"
	"github.com/prismgo/framework/filesystem"
	"github.com/prismgo/framework/foundation"
	"github.com/prismgo/horizon"
	"github.com/prismgo/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// ServiceProvider installs admin services and SQL drivers.
type ServiceProvider struct{ Configure func(*admin.Options) }

// Register declares lazy bindings without resolving infrastructure.
func (p ServiceProvider) Register(app pc.Application) error {
	if err := (horizon.ServiceProvider{}).Register(app); err != nil {
		return err
	}
	return (providers.AdminServiceProvider{Configure: p.Configure}).Register(app)
}

// Boot 注册数据库驱动与后台命令。
func (p ServiceProvider) Boot(app pc.Application) error {
	if err := (horizon.ServiceProvider{}).Boot(app); err != nil {
		return err
	}
	if err := (sqlite.ServiceProvider{}).Boot(app); err != nil {
		return err
	}
	files, err := filesystem.ManagerFrom(app.Container())
	if err != nil {
		return err
	}
	storage.Register(files)
	manager, err := database.ManagerFrom(app.Container())
	if err != nil {
		return err
	}
	for _, driver := range []string{"pgsql", "postgres"} {
		manager.Extend(driver, func(c database.DriverContext) (gorm.Dialector, error) { return postgres.Open(c.DSN), nil })
	}
	manager.Extend("mariadb", func(c database.DriverContext) (gorm.Dialector, error) { return mysql.Open(c.DSN), nil })
	return (providers.AdminServiceProvider{}).Boot(app)
}

// RegisterRoutes mounts the module in the application's official HTTP pipeline.
func RegisterRoutes(app *foundation.Application, engine *gin.Engine) error {
	return routes.Register(app, engine)
}
