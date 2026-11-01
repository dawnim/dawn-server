package tests

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/constants"
	"github.com/dawnim/dawn-server/database/migrations"
	"github.com/prismgo/framework/encryption"
)

func TestSecretSettings(t *testing.T) {
	r := miniredis.RunT(t)
	a, err := newTestApp(testOptions{DBDSN: ":memory:", Storage: t.TempDir(), RedisURL: "redis://" + r.Addr(), EncryptionKey: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := migrations.Install(a.DB); err != nil {
		t.Fatal(err)
	}
	a.SettingDefinitions["app.secret"] = admin.SettingDefinition{Key: "app.secret", Kind: "string", Secret: true, Default: "default-secret"}
	ctx := context.Background()
	value, err := a.Setting(ctx, "app.secret")
	if err != nil || string(value) != `"default-secret"` {
		t.Fatalf("保密配置默认值: %s %v", value, err)
	}
	ciphertext, err := encryption.EncryptString(ctx, `"configured-secret"`)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"secret": ciphertext})
	row := models.Setting{Key: "app", Name: "业务配置", Value: string(raw), Revision: 1}
	if err := a.DB.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		value, err := a.Setting(ctx, "app.secret")
		if err != nil || string(value) != `"configured-secret"` {
			t.Fatalf("保密配置读取: %s %v", value, err)
		}
	}
	key := a.Key(constants.ForeverSettings, "app")
	if !r.Exists(key) || r.HGet(key, "secret") == "" || strings.Contains(r.HGet(key, "secret"), "configured-secret") {
		t.Fatal("分组Hash未保存密文")
	}
	if r.HGet(key, "__revision") == "" {
		t.Fatal("分组缓存缺少版本")
	}
	var saved models.Setting
	if err := a.DB.First(&saved, "key = ?", row.Key).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Value != string(raw) || saved.UpdatedAt != row.UpdatedAt {
		t.Fatal("读取配置不应修改数据库")
	}
	a.SettingDefinitions["app.secret"] = admin.SettingDefinition{Key: "app.secret", Title: "API secret", Group: "integration", Kind: "string", Input: "password", Secret: true, Default: "new-default"}
	if err := a.SyncSettings(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.First(&saved, "key = ?", row.Key).Error; err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err := json.Unmarshal([]byte(saved.Value), &values); err != nil {
		t.Fatal(err)
	}
	if values["secret"] != ciphertext || saved.Name != "integration" || saved.CreatedAt == 0 {
		t.Fatal("同步定义覆盖了值或丢失元数据", saved.Key)
	}
	if strings.Contains(saved.Value, "configured-secret") {
		t.Fatal("配置密文泄露")
	}
	old := saved
	old.Revision = 0
	old.Value = `{"secret":"stale"}`
	if err := a.CacheSetting(ctx, old); err != nil {
		t.Fatal(err)
	}
	if value, err := a.Setting(ctx, "app.secret"); err != nil || string(value) != `"configured-secret"` {
		t.Fatal("旧缓存覆盖了新值", err)
	}
	siteKey := a.Key(constants.ForeverSettings, "site")
	if !r.Exists(siteKey) || r.HGet(siteKey, "title") != `"Dawn"` || siteKey == key {
		t.Fatal("各配置组应独立保存Hash，参数应使用独立Field")
	}
	if err := a.Redis.Del(ctx, key).Err(); err != nil {
		t.Fatal(err)
	}
	if value, err := a.Setting(ctx, "app.secret"); err != nil || string(value) != `"configured-secret"` {
		t.Fatal("清理分组缓存后应从数据库恢复", err)
	}
	if !r.Exists(key) || r.HGet(siteKey, "title") != `"Dawn"` {
		t.Fatal("重建缓存不应影响其他组")
	}
	a.SettingDefinitions["app.invalid"] = admin.SettingDefinition{Key: "app.invalid", Kind: "string", Secret: true}
	raw, _ = json.Marshal(map[string]string{"secret": ciphertext, "invalid": "invalid-ciphertext"})
	saved.Value, saved.Revision = string(raw), saved.Revision+1
	if err := a.DB.Save(&saved).Error; err != nil {
		t.Fatal(err)
	}
	if err := a.CacheSetting(ctx, saved); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Setting(ctx, "app.invalid"); err == nil {
		t.Fatal("无效密文必须报错")
	}
}
