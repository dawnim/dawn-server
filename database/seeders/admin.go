package seeders

import (
	"encoding/json"
	"strings"

	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/constants"
	"github.com/dawnim/dawn-server/support/i18n"
	"github.com/prismgo/framework/config"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Seed 补齐系统基础数据，不覆盖已有账号、配置值和自定义分类。
func Seed(db *gorm.DB) error {
	a := admin.Resolve()
	ctx := db.Statement.Context
	defaults := map[string]map[string]json.RawMessage{}
	names := map[string]string{}
	for _, definition := range a.SettingDefinitions {
		group, field, _ := strings.Cut(definition.Key, ".")
		if defaults[group] == nil {
			defaults[group] = map[string]json.RawMessage{}
		}
		value, err := json.Marshal(definition.Default)
		if err != nil {
			return err
		}
		if definition.Secret {
			value = json.RawMessage(`""`)
		}
		defaults[group][field] = value
		names[group] = i18n.Text("zh", definition.Group)
	}
	var user models.User
	if err := db.Transaction(func(tx *gorm.DB) error {
		department := models.Department{Name: "Dawn", Status: models.Enabled}
		if err := tx.Where("name = ? AND parent_id = ?", department.Name, 0).FirstOrCreate(&department).Error; err != nil {
			return err
		}
		post := models.Post{Name: "CEO", Code: "CEO", Status: models.Enabled}
		if err := tx.Where("code = ?", post.Code).FirstOrCreate(&post).Error; err != nil {
			return err
		}
		username := config.GetString("dawn.admin_username", "admin")
		if err := tx.Where("username = ?", username).Limit(1).Find(&user).Error; err != nil {
			return err
		}
		if user.ID == 0 {
			password := config.GetString("dawn.admin_password", admin.DefaultAdminPassword)
			if len(password) < 6 || len(password) > 72 {
				return i18n.Errorf("密码长度须为6至72字符")
			}
			hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
			if err != nil {
				return err
			}
			user = models.User{Username: username, Password: string(hash), Realname: "超级管理员", Status: models.Enabled, Super: true, DepartmentID: department.ID}
			if err := tx.Create(&user).Error; err != nil {
				return err
			}
			if err := tx.Model(&user).Association("Posts").Append(&post); err != nil {
				return err
			}
		}
		for i, item := range []struct{ code, name string }{{"image", "图片"}, {"video", "视频"}, {"audio", "音频"}, {"document", "文档"}, {"archive", "压缩包"}, {"other", "其他"}} {
			category := models.Category{Code: &item.code, Name: item.name, Sort: i * 10}
			if err := tx.Where("code = ?", item.code).FirstOrCreate(&category).Error; err != nil {
				return err
			}
		}
		for group, values := range defaults {
			raw, err := json.Marshal(values)
			if err != nil {
				return err
			}
			row := models.Setting{Key: group, Name: names[group], Value: string(raw), Revision: 1}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoNothing: true}).Create(&row).Error; err != nil {
				return err
			}
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(map[string]any{"key": group}).First(&row).Error; err != nil {
				return err
			}
			saved := map[string]json.RawMessage{}
			if err := json.Unmarshal([]byte(row.Value), &saved); err != nil {
				return err
			}
			for field, value := range values {
				if _, exists := saved[field]; !exists {
					saved[field] = value
				}
			}
			raw, err = json.Marshal(saved)
			if err != nil {
				return err
			}
			if row.Value != string(raw) || row.Name != names[group] {
				row.Name, row.Value, row.Revision = names[group], string(raw), row.Revision+1
				if err := tx.Save(&row).Error; err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		return err
	}
	// 数据提交后失效缓存，读取时按实际连接重建，支持db:seed --database。
	keys := make([]string, 0, len(defaults))
	for group := range defaults {
		keys = append(keys, a.Key(constants.ForeverSettings, group))
	}
	if err := a.Redis.Del(ctx, keys...).Err(); err != nil {
		return err
	}
	return a.ClearUserCaches(ctx, user.ID)
}
