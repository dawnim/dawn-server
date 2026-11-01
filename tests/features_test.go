package tests

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/app/admin/middleware"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/constants"
	"github.com/dawnim/dawn-server/database/migrations"
	"github.com/dawnim/dawn-server/support/i18n"
	"github.com/gin-gonic/gin"
	"github.com/prismgo/horizon"
)

func TestAdminFeatures(t *testing.T) {
	redis := miniredis.RunT(t)
	root := t.TempDir()
	a, err := newTestApp(testOptions{Environment: "local", DBDSN: filepath.Join(root, "data.sqlite"), Storage: root, RedisURL: "redis://" + redis.Addr(), EncryptionKey: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if text := i18n.Text("en", "没有译文的提示%s", "测试"); text != "没有译文的提示测试" {
		t.Fatal("缺少译文应使用原文并填入参数", text)
	}
	ctx := context.Background()
	if err := migrations.Run(ctx, a); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := a.InstallDefaults(ctx, "admin", "123456"); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []struct {
		model any
		count int64
	}{{&models.Department{}, 1}, {&models.Post{}, 1}, {&models.UserPost{}, 1}, {&models.Category{}, 5}, {&models.Setting{}, 3}} {
		var count int64
		a.DB.Model(item.model).Count(&count)
		if count != item.count {
			t.Fatalf("初始化重复或缺少数据 %T %d", item.model, count)
		}
	}
	var setting models.Setting
	a.DB.Where(map[string]any{"key": "upload"}).First(&setting)
	if setting.Name != "上传配置" || !json.Valid([]byte(setting.Value)) {
		t.Fatal("配置组名称或JSON错误", setting.Name)
	}
	if redis.HGet(a.Key(constants.ForeverSettings, "upload"), "driver") == "" {
		t.Fatal("未初始化配置缓存")
	}
	tokenValue := rand.Text()
	session := models.Session{ID: rand.Text(), UserID: 1, AccessHash: admin.TokenHash(tokenValue), RefreshHash: admin.TokenHash(rand.Text()), ExpiresAt: time.Now().Add(time.Hour).UnixMilli(), RefreshExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
	if err := a.DB.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	handler, err := a.Handler()
	if err != nil {
		t.Fatal(err)
	}
	a.Router.POST("/admin/test/audit", middleware.Require(a, ""), func(c *gin.Context) { c.JSON(200, gin.H{"data": gin.H{"token": "secret-response", "safe": "yes"}}) })
	call := func(method, path string, input any) (int, map[string]any) {
		t.Helper()
		data, _ := json.Marshal(input)
		req := httptest.NewRequest(method, "/admin"+path, bytes.NewReader(data))
		req.Header.Set("Authorization", "Bearer "+tokenValue)
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		var result map[string]any
		_ = json.Unmarshal(res.Body.Bytes(), &result)
		return res.Code, result
	}
	if status, result := call("POST", "/test/audit", gin.H{"password": "secret-request", "nested": gin.H{"secret": "nested-secret"}, "safe": "yes"}); status != 200 {
		t.Fatal(result)
	}
	var log models.Log
	a.DB.Where("path = ?", "/admin/test/audit").First(&log)
	if !strings.Contains(log.RequestData, "secret-request") || !strings.Contains(log.RequestData, "nested-secret") || !strings.Contains(log.ResponseData, "secret-response") {
		t.Fatal("审计正文未保留原值")
	}
	for _, kind := range []string{"string", "hash"} {
		definition := constants.Definition{Name: "TempPublic" + kind, Pattern: "temp:public:" + kind, Type: kind, Readable: true, Editable: true, Clearable: true}
		a.CacheKeys[definition.Name] = definition
		key := a.Key(definition)
		if kind == "string" {
			a.Redis.Set(ctx, key, "old", time.Minute)
		} else {
			a.Redis.HSet(ctx, key, "name", "old")
			a.Redis.Expire(ctx, key, time.Minute)
		}
		path := "/cache/value?definition=" + definition.Name + "&key=" + key
		if status, result := call("PUT", path, gin.H{"field": "name", "value": "new"}); status != 200 {
			t.Fatal(result)
		}
		if ttl := a.Redis.TTL(ctx, key).Val(); ttl <= 0 {
			t.Fatal("编辑丢失TTL")
		}
		if status, result := call("GET", path, nil); status != 200 || !strings.Contains(fmt.Sprint(result), "new") {
			t.Fatal(result)
		}
	}
	a.Redis.Set(ctx, a.Key(constants.TempCaptcha, "protected"), "keep", time.Minute)
	if status, result := call("DELETE", "/cache/clear", nil); status != 200 {
		t.Fatal(result)
	}
	if !redis.Exists(a.Key(constants.TempCaptcha, "protected")) || redis.Exists(a.Options.Name+":"+a.Options.Environment+":temp:public:string") {
		t.Fatal("批量清理未遵循Clearable范围")
	}
	key := a.Key(constants.TempCaptcha, "protected")
	for _, query := range []url.Values{
		{"definition": {"Unknown"}, "key": {key}},
		{"definition": {"ForeverSettings"}, "key": {key}},
		{"definition": {"TempCaptcha"}, "key": {"other:" + key}},
	} {
		if status, result := call("DELETE", "/cache/value?"+query.Encode(), nil); status != 422 {
			t.Fatal("单项删除允许未注册或不属于定义的Key", result)
		}
	}
	if status, result := call("DELETE", "/cache/value?definition=TempCaptcha&key="+url.QueryEscape(key), nil); status != 200 || redis.Exists(key) {
		t.Fatal("Clearable=false的Key应允许手动删除", result)
	}
	if status, result := call("GET", "/system/server", nil); status != 200 || result["data"].(map[string]any)["pid"].(float64) <= 0 {
		t.Fatal(result)
	}
	if status, result := call("GET", "/system/horizon/status", nil); status != 200 || result["data"].(map[string]any)["status"] == nil {
		t.Fatal(result)
	}
	store, err := horizon.Resolve().ResolveStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	host := `host "ID": "KeepUpper"`
	if err := store.HeartbeatMaster(ctx, horizon.MasterState{ID: "field-names", Host: host, PID: 123, StartedAt: time.Now(), LastHeartbeatAt: time.Now(), Environment: "local"}); err != nil {
		t.Fatal(err)
	}
	status, result := call("GET", "/system/horizon/masters", nil)
	if status != 200 {
		t.Fatal(result)
	}
	master := result["data"].([]any)[0].(map[string]any)
	if master["id"] != "field-names" || master["host"] != host || master["started_at"] == nil || master["ID"] != nil || master["StartedAt"] != nil {
		t.Fatal("队列监控字段未统一或误改了字符串内容", master)
	}
	if status, result := call("PUT", "/auth/profile", gin.H{"realname": "个人资料", "email": "profile@example.com", "phone": "123"}); status != 200 {
		t.Fatal(result)
	}
	if status, result := call("PUT", "/auth/profile", gin.H{"avatar": "/avatar.png"}); status != 200 {
		t.Fatal(result)
	}
	status, result = call("GET", "/auth/me", nil)
	profile := result["data"].(map[string]any)
	if status != 200 || profile["avatar"] != "/avatar.png" || profile["realname"] != "个人资料" || profile["email"] != "profile@example.com" || profile["phone"] != "123" || profile["department"].(map[string]any)["name"] == "" {
		t.Fatal("单独更新头像覆盖了资料或未返回部门", result)
	}
	if status, result := call("PUT", "/auth/profile", gin.H{"phone": "", "email": ""}); status != 200 {
		t.Fatal(result)
	}
	status, result = call("GET", "/auth/me", nil)
	if status != 200 || result["data"].(map[string]any)["phone"] != "" || result["data"].(map[string]any)["email"] != "" {
		t.Fatal("无法清空个人资料字段", result)
	}
	if err := a.DB.Exec("CREATE TABLE business_widgets(id INTEGER PRIMARY KEY, name TEXT, created_at BIGINT, updated_at BIGINT, deleted_at BIGINT)").Error; err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"simple", "full"} {
		status, result := call("GET", "/development/generate?table=business_widgets&mode="+mode, nil)
		if status != 200 {
			t.Fatal(mode, result)
		}
		files := result["data"].(map[string]any)
		if _, ok := files["admin/src/api/business/business_widgets.ts"]; !ok {
			t.Fatal("缺少独立API文件")
		}
		for _, locale := range []string{"zh", "en"} {
			file := "admin/src/locales/" + locale + "/business_widgets.ts"
			var labels map[string]string
			if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSpace(files[file].(string)), "export default ")), &labels); err != nil {
				t.Fatal(err)
			}
			if labels["name"] == "" || labels["created_at"] == "" {
				t.Fatal("生成语言包缺少字段标签", locale)
			}
		}
		for path := range files {
			if strings.HasPrefix(path, "database/migrations/") {
				t.Fatal("未勾选时不应生成迁移")
			}
		}
		page := files["admin/src/views/business/business_widgets/index.vue"].(string)
		if mode == "simple" && !strings.Contains(page, "<CrudPage") {
			t.Fatal("引用模式未使用内置页面")
		}
		if mode == "full" {
			for _, name := range []string{"index.vue", "modules/table-search.vue", "modules/edit-dialog.vue"} {
				if _, ok := files["admin/src/views/business/business_widgets/"+name]; !ok {
					t.Fatal("源码模式缺少文件", name)
				}
			}
			if !strings.Contains(page, "<ArtTableHeader") || strings.Contains(page, "<CrudPage") {
				t.Fatal("源码模式未输出原版列表")
			}
		}
		for name, source := range result["data"].(map[string]any) {
			if strings.HasSuffix(name, ".go") {
				if _, err := parser.ParseFile(token.NewFileSet(), name, source.(string), parser.AllErrors); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	status, generated := call("GET", "/development/generate?table=business_widgets&mode=full&migration=1", nil)
	if status != 200 {
		t.Fatal(generated)
	}
	count := 0
	for name, source := range generated["data"].(map[string]any) {
		if strings.HasPrefix(name, "database/migrations/") {
			count++
			if _, err := parser.ParseFile(token.NewFileSet(), name, source.(string), parser.AllErrors); err != nil {
				t.Fatal(err)
			}
		}
	}
	if count != 1 {
		t.Fatal("勾选后应生成一份迁移", count)
	}
	files := generated["data"].(map[string]any)
	controllerPath := "app/admin/controllers/business_widgets.go"
	files[controllerPath] = files[controllerPath].(string) + "\n// edited in generator\n"
	input := map[string]any{"files": files}
	status, result = call("POST", "/development/generate?table=business_widgets&mode=full&migration=1", input)
	if status != 200 {
		t.Fatal(result)
	}
	file := filepath.Join(root, "app/admin/controllers/business_widgets.go")
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if status, result := call("POST", "/development/generate?table=business_widgets&mode=full&migration=1", input); status != 409 {
		t.Fatal("允许覆盖文件", result)
	}
	after, _ := os.ReadFile(file)
	if !strings.Contains(string(before), "edited in generator") {
		t.Fatal("未写入编辑后的内容")
	}
	if !bytes.Equal(before, after) {
		t.Fatal("已存在文件被修改")
	}
}
