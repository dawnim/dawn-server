package tests

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/constants"
	"github.com/prismgo/framework/console"
	"golang.org/x/crypto/bcrypt"
)

// 验证原生命令的登记、结构与数据分离、回滚及重新安装。
func TestNativeDatabaseLifecycle(t *testing.T) {
	store := miniredis.RunT(t)
	a, err := newTestApp(testOptions{DBDSN: ":memory:", Storage: t.TempDir(), RedisURL: "redis://" + store.Addr()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, command := range []string{"migrate", "migrate", "migrate:status"} {
		if err := databaseCommand(a, command); err != nil {
			t.Fatal(command, err)
		}
	}
	var records []struct {
		Migration string
		Batch     int
	}
	if err := a.DB.Table("migrations").Find(&records).Error; err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Migration != "202610300000_install_dawn" || records[0].Batch != 1 || a.DB.Migrator().HasTable("admin_migrations") {
		t.Fatal("未使用唯一的原生迁移登记", records)
	}
	for _, model := range []any{&models.ChatConversation{}, &models.ChatMember{}, &models.ChatMessage{}, &models.ChatEvent{}} {
		if !a.DB.Migrator().HasTable(model) {
			t.Fatalf("扩展安装缺少聊天表 %T", model)
		}
	}
	for _, item := range []struct {
		model   any
		name    string
		columns []string
	}{
		{&models.ChatEvent{}, "idx_chat_event_pending", []string{"published_at", "available_at", "id"}},
		{&models.ChatMessage{}, "idx_chat_message_sender_time", []string{"sender_realm", "sender_subject", "created_at"}},
	} {
		indexes, err := a.DB.Migrator().GetIndexes(item.model)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, index := range indexes {
			if index.Name() == item.name {
				found = true
				if !slices.Equal(index.Columns(), item.columns) {
					t.Fatal("聊天索引字段或顺序错误", item.name, index.Columns())
				}
			}
		}
		if !found {
			t.Fatal("扩展安装缺少聊天索引", item.name)
		}
	}
	var count int64
	if err := a.DB.Model(&models.User{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("迁移混入了初始化数据", count, err)
	}
	if err := databaseCommand(a, "db:seed"); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		model any
		count int64
	}{{&models.User{}, 1}, {&models.Department{}, 1}, {&models.Post{}, 1}, {&models.UserPost{}, 1}, {&models.Category{}, 6}, {&models.Setting{}, 3}} {
		if err := a.DB.Model(item.model).Count(&count).Error; err != nil || count != item.count {
			t.Fatalf("初始数据 %T: %d %v", item.model, count, err)
		}
	}
	var user models.User
	if err := a.DB.Preload("Posts").First(&user).Error; err != nil {
		t.Fatal(err)
	}
	if user.Username != "admin" || !user.Super || user.Status != models.Enabled || user.Dashboard != "statistics" || len(user.Posts) != 1 || bcrypt.CompareHashAndPassword([]byte(user.Password), []byte("123456")) != nil {
		t.Fatal("默认管理员数据不正确")
	}
	if err := a.DB.Exec("CREATE TABLE business_records (id INTEGER PRIMARY KEY)").Error; err != nil {
		t.Fatal(err)
	}
	if err := databaseCommand(a, "migrate:rollback"); err != nil {
		t.Fatal(err)
	}
	tables, err := a.DB.Migrator().GetTables()
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		if strings.HasPrefix(table, "admin_") || strings.HasPrefix(table, "chat_") {
			t.Fatal("回滚残留系统表", table)
		}
	}
	if !a.DB.Migrator().HasTable("business_records") {
		t.Fatal("回滚删除了业务表")
	}
	if err := a.DB.Table("migrations").Count(&count).Error; err != nil || count != 0 {
		t.Fatal("回滚未清除迁移记录", count, err)
	}
	for _, command := range []string{"dawn:install", "dawn:install", "migrate:fresh --seed"} {
		if err := databaseCommand(a, command); err != nil {
			t.Fatal(command, err)
		}
	}
	if err := a.DB.Model(&models.User{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("重新安装失败", count, err)
	}
}

func TestNativeSeederPreservesData(t *testing.T) {
	store := miniredis.RunT(t)
	a, err := newTestApp(testOptions{DBDSN: ":memory:", Storage: t.TempDir(), RedisURL: "redis://" + store.Addr(), AdminUsername: "owner", AdminPassword: "initial-secret"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := databaseCommand(a, "dawn:install"); err != nil {
		t.Fatal(err)
	}
	var user models.User
	if err := a.DB.First(&user).Error; err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("changed-secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Model(&user).Updates(map[string]any{"password": string(hash), "super": false, "status": 2, "department_id": 0, "realname": "自定义", "dashboard": "work"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Model(&user).Association("Posts").Clear(); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Model(&models.Category{}).Where("code = ?", "image").Updates(map[string]any{"name": "相册", "sort": 99}).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Create(&models.Category{Name: "自建分类"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Model(&models.Setting{}).Where(map[string]any{"key": "upload"}).Update("value", `{"driver":"custom","custom":123}`).Error; err != nil {
		t.Fatal(err)
	}
	a.SettingDefinitions["upload.extra"] = admin.SettingDefinition{Key: "upload.extra", Group: "上传配置", Kind: "string", Default: "added"}
	a.SettingDefinitions["upload.secret"] = admin.SettingDefinition{Key: "upload.secret", Group: "上传配置", Kind: "string", Secret: true, Default: "must-not-store"}
	ctx := context.Background()
	for _, key := range []string{a.Key(constants.ForeverSettings, "upload"), a.Key(constants.TempUserInfo, user.ID), a.Key(constants.TempUserPermissions, user.ID), a.Key(constants.TempUserMenus, user.ID)} {
		if err := a.Redis.Set(ctx, key, "stale", 0).Err(); err != nil {
			t.Fatal(err)
		}
	}
	if err := databaseCommand(a, "db:seed --class=DatabaseSeeder"); err != nil {
		t.Fatal(err)
	}
	if store.Exists(a.Key(constants.ForeverSettings, "upload")) || store.Exists(a.Key(constants.TempUserInfo, user.ID)) || store.Exists(a.Key(constants.TempUserPermissions, user.ID)) || store.Exists(a.Key(constants.TempUserMenus, user.ID)) {
		t.Fatal("初始化后仍有旧缓存")
	}
	if err := a.DB.Preload("Posts").First(&user).Error; err != nil {
		t.Fatal(err)
	}
	if user.Username != "owner" || user.Password != string(hash) || user.Super || user.Status != 2 || user.DepartmentID != 0 || user.Realname != "自定义" || user.Dashboard != "work" || len(user.Posts) != 0 {
		t.Fatal("重复初始化覆盖了账号设置")
	}
	var category models.Category
	if err := a.DB.First(&category, "code = ?", "image").Error; err != nil || category.Name != "相册" || category.Sort != 99 {
		t.Fatal("重复初始化覆盖了分类", err)
	}
	var setting models.Setting
	if err := a.DB.Where(map[string]any{"key": "upload"}).First(&setting).Error; err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err := json.Unmarshal([]byte(setting.Value), &values); err != nil {
		t.Fatal(err)
	}
	if values["driver"] != "custom" || values["custom"] != float64(123) || values["extra"] != "added" || values["secret"] != "" {
		t.Fatal("未正确补齐配置", values)
	}
	revision, updatedAt := setting.Revision, setting.UpdatedAt
	if err := databaseCommand(a, "db:seed"); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.First(&setting, "id = ?", setting.ID).Error; err != nil || setting.Revision != revision || setting.UpdatedAt != updatedAt {
		t.Fatal("重复初始化无意义地更新配置", err)
	}
	var count int64
	if err := a.DB.Model(&models.User{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("重复创建了账号", count, err)
	}
	if err := a.DB.Model(&models.Category{}).Count(&count).Error; err != nil || count != 7 {
		t.Fatal("分类重复或丢失", count, err)
	}
}

func TestNativeSeedPasswordAndProduction(t *testing.T) {
	for _, password := range []string{"12345", "123456", strings.Repeat("x", 72), strings.Repeat("x", 73)} {
		t.Run(strconv.Itoa(len(password)), func(t *testing.T) {
			store := miniredis.RunT(t)
			a, err := newTestApp(testOptions{DBDSN: ":memory:", Storage: t.TempDir(), RedisURL: "redis://" + store.Addr(), AdminPassword: password})
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			if err := databaseCommand(a, "migrate"); err != nil {
				t.Fatal(err)
			}
			err = databaseCommand(a, "db:seed")
			valid := len(password) >= 6 && len(password) <= 72
			if (err == nil) != valid {
				t.Fatal("密码长度边界", len(password), err)
			}
			if !valid {
				var count int64
				if err := a.DB.Model(&models.Department{}).Count(&count).Error; err != nil || count != 0 {
					t.Fatal("失败的seed未回滚", count, err)
				}
			}
		})
	}
	store := miniredis.RunT(t)
	a, err := newTestApp(testOptions{DBDSN: ":memory:", Storage: t.TempDir(), RedisURL: "redis://" + store.Addr(), Environment: "production"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	for _, command := range []string{"migrate", "db:seed", "dawn:install"} {
		if err := databaseCommand(a, command); err == nil {
			t.Fatal("生产环境未要求force", command)
		}
	}
	if a.DB.Migrator().HasTable("admin_users") {
		t.Fatal("生产环境保护失败")
	}
	if err := databaseCommand(a, "dawn:install", console.CallInput{Options: map[string]any{"force": true}}); err != nil {
		t.Fatal("安装未转发force", err)
	}
}
