package tests

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/database/migrations"
)

type generatedItem struct {
	models.Base
	Name string
}

func (generatedItem) TableName() string { return "app_example_items" }

func TestGeneratorUsesApplicationDirectories(t *testing.T) {
	r := miniredis.RunT(t)
	a, err := newTestApp(testOptions{DBDSN: ":memory:", Storage: t.TempDir(), RedisURL: "redis://" + r.Addr()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := migrations.Install(a.DB); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.AutoMigrate(&generatedItem{}); err != nil {
		t.Fatal(err)
	}
	user := models.User{Username: "generator", Super: true, Status: models.Enabled}
	if err := a.DB.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	token := rand.Text()
	if err := a.DB.Create(&models.Session{ID: rand.Text(), UserID: user.ID, AccessHash: admin.TokenHash(token), RefreshHash: rand.Text(), ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}).Error; err != nil {
		t.Fatal(err)
	}
	handler, err := a.Handler()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/admin/development/generate?table=app_example_items", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("generate status=%d body=%s", response.Code, response.Body.String())
	}
	var result struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Data["app/admin/controllers/app_example_items.go"], "example.com/test/app/models") || !strings.Contains(result.Data["app/admin/controllers/app_example_items.go"], "github.com/dawnim/dawn-server/app/admin") {
		t.Fatalf("controller imports: %s", result.Data["app/admin/controllers/app_example_items.go"])
	}
	if !strings.Contains(result.Data["app/models/app_example_items.go"], "models.Base") {
		t.Fatal("generated model missing Base")
	}
	if len(result.Data) != 7 {
		t.Fatalf("generated files=%d want=7", len(result.Data))
	}
	apiPath := "admin/src/api/business/app_example_items.ts"
	result.Data[apiPath] += "\n// edited before download\n"
	body, _ := json.Marshal(map[string]any{"files": result.Data})
	request = httptest.NewRequest("POST", "/admin/development/export?table=app_example_items", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatal("下载编辑内容失败", response.Code, response.Body.String())
	}
	archive, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, file := range archive.File {
		if file.Name == apiPath {
			reader, err := file.Open()
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(reader)
			reader.Close()
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != result.Data[apiPath] {
				t.Fatal("ZIP未保存用户编辑")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("ZIP缺少API文件")
	}
	delete(result.Data, apiPath)
	result.Data["../../outside.go"] = "outside"
	body, _ = json.Marshal(map[string]any{"files": result.Data})
	request = httptest.NewRequest("POST", "/admin/development/export?table=app_example_items", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 422 {
		t.Fatal("允许提交生成范围以外的路径", response.Code)
	}
	for _, table := range []string{"orders", "shop_orders", "admin_orders"} {
		if err := a.DB.Table(table).AutoMigrate(&generatedItem{}); err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest("GET", "/admin/development/generate?table="+table, nil)
		request.Header.Set("Authorization", "Bearer "+token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 200 {
			t.Fatalf("业务表%s不应受前缀限制: %d %s", table, response.Code, response.Body.String())
		}
	}
	request = httptest.NewRequest("GET", "/admin/development/generate?table=admin_users", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 422 {
		t.Fatal("扩展内置表不应生成业务CRUD", response.Code)
	}
}
