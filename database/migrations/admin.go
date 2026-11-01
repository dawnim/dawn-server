package migrations

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"github.com/dawnim/dawn-server/support/i18n"

	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/constants"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Run 在显式命令中执行编译进二进制的增量迁移, 不依赖源码目录扫描.
func Run(ctx context.Context, a *admin.App) (result error) {
	key, token := a.Key(constants.LockMigration), rand.Text()
	locked, err := a.Redis.SetNX(ctx, key, token, constants.LockMigration.TTL).Result()
	if err != nil {
		return err
	}
	if !locked {
		return i18n.Errorf("另一个实例正在执行迁移")
	}
	defer func() {
		err := a.Redis.Eval(context.WithoutCancel(ctx), `if redis.call('get',KEYS[1]) == ARGV[1] then return redis.call('del',KEYS[1]) end return 0`, []string{key}, token).Err()
		result = errors.Join(result, err)
	}()
	if err := Install(a.DB.WithContext(ctx)); err != nil {
		return err
	}
	return a.SyncSettings(ctx)
}

// Install applies versioned SDK schema changes on the existing connection.
func Install(db *gorm.DB) error {
	if !db.Migrator().HasTable(&models.Migration{}) {
		if err := db.Migrator().CreateTable(&models.Migration{}); err != nil {
			return err
		}
	}
	var count int64
	if err := db.Model(&models.Migration{}).Where("name = ?", "admin.0001").Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	if err := db.SetupJoinTable(&models.User{}, "Roles", &models.UserRole{}); err != nil {
		return err
	}
	if err := db.SetupJoinTable(&models.User{}, "Posts", &models.UserPost{}); err != nil {
		return err
	}
	tables := []struct {
		model   interface{ TableName() string }
		comment string
	}{
		{&models.Department{}, "部门"}, {&models.Post{}, "岗位"}, {&models.Role{}, "角色"}, {&models.User{}, "管理员"},
		{&models.UserRole{}, "管理员与角色关联"}, {&models.UserPost{}, "管理员与岗位关联"},
		{&models.RolePermission{}, "角色权限"}, {&models.Session{}, "管理员登录会话"},
		{&models.Setting{}, "全局配置"}, {&models.Menu{}, "自定义菜单与权限"},
		{&models.File{}, "文件"}, {&models.Category{}, "附件分类"}, {&models.Attachment{}, "附件"},
		{&models.Log{}, "后台日志"}, {&models.Migration{}, "后台结构迁移记录"},
	}
	for _, table := range tables {
		if !db.Migrator().HasTable(table.model) {
			if err := db.Migrator().CreateTable(table.model); err != nil {
				return err
			}
		}
	}
	for _, table := range tables {
		var err error
		switch db.Dialector.Name() {
		case "mysql":
			err = db.Exec("ALTER TABLE ? COMMENT = ?", clause.Table{Name: table.model.TableName()}, gorm.Expr(db.Dialector.Explain("?", table.comment))).Error
		case "postgres":
			err = db.Exec("COMMENT ON TABLE ? IS ?", clause.Table{Name: table.model.TableName()}, gorm.Expr(db.Dialector.Explain("$1", table.comment))).Error
		}
		if err != nil {
			return err
		}
	}
	return db.Create(&models.Migration{Name: "admin.0001", CreatedAt: time.Now().UnixMilli()}).Error
}
