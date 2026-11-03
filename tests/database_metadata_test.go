package tests

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/app/models"
)

func TestDatabaseMetadata(t *testing.T) {
	redis := miniredis.RunT(t)
	root := t.TempDir()
	driver, dsn := "sqlite", filepath.Join(root, "metadata.sqlite")
	if os.Getenv("DAWN_TEST_DRIVER") != "" && os.Getenv("DAWN_TEST_DSN") != "" {
		driver, dsn = os.Getenv("DAWN_TEST_DRIVER"), os.Getenv("DAWN_TEST_DSN")
	}
	a, err := newTestApp(testOptions{AdminUsername: "metadata_admin", AdminPassword: "test-password", DBDriver: driver, DBDSN: dsn, Storage: root, RedisURL: "redis://" + redis.Addr()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := databaseCommand(a, "migrate"); err != nil {
		t.Fatal(err)
	}
	if err := databaseCommand(a, "db:seed"); err != nil {
		t.Fatal(err)
	}
	var user models.User
	if err := a.DB.Where("username = ?", "metadata_admin").First(&user).Error; err != nil {
		t.Fatal(err)
	}
	token := rand.Text()
	expires := time.Now().Add(time.Hour).UnixMilli()
	if err := a.DB.Create(&models.Session{ID: rand.Text(), UserID: user.ID, AccessHash: admin.TokenHash(token), RefreshHash: admin.TokenHash(rand.Text()), ExpiresAt: expires, RefreshExpiresAt: expires}).Error; err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		`CREATE TABLE metadata_records (id BIGINT PRIMARY KEY, code VARCHAR(64) UNIQUE,
			bucket VARCHAR(32), region VARCHAR(32), amount DECIMAL(12,2) DEFAULT 0,
			enabled INT NOT NULL DEFAULT 0, note VARCHAR(200) DEFAULT '', UNIQUE(bucket, region))`,
		`CREATE INDEX idx_metadata_region ON metadata_records(region)`,
		`INSERT INTO metadata_records(id, code) VALUES (1, 'one'), (2, 'two')`,
	} {
		if err := a.DB.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	handler, err := a.Handler()
	if err != nil {
		t.Fatal(err)
	}
	call := func(path, access string) (int, map[string]any) {
		t.Helper()
		request := httptest.NewRequest("GET", "/admin"+path, nil)
		request.Header.Set("Authorization", "Bearer "+access)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		var result map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &result); response.Body.Len() > 0 && err != nil {
			t.Fatal(err)
		}
		return response.Code, result
	}
	status, result := call("/tables", token)
	if status != 200 {
		t.Fatal(result)
	}
	var info map[string]any
	for _, item := range result["data"].([]any) {
		row := item.(map[string]any)
		if row["name"] == "metadata_records" {
			info = row
		}
	}
	for _, field := range []string{"name", "comment", "engine", "rows", "data_free", "data_length", "index_length", "collation", "created_at", "updated_at"} {
		if _, ok := info[field]; !ok {
			t.Fatal("表列表缺少字段", field, info)
		}
	}
	switch a.DB.Dialector.Name() {
	case "mysql":
		if info["engine"] != "InnoDB" || info["data_length"] == nil || info["collation"] == nil || info["created_at"].(float64) < 1e12 {
			t.Fatal("MySQL未返回真实表元数据或毫秒时间", info)
		}
	case "postgres":
		if info["engine"] != "heap" || info["data_length"] == nil || info["data_free"] != nil || info["created_at"] != nil {
			t.Fatal("PostgreSQL表元数据不正确", info)
		}
	case "sqlite":
		if info["rows"] != float64(2) || info["data_length"] != nil || info["created_at"] != nil {
			t.Fatal("SQLite未知元数据被伪造", info)
		}
	}
	status, result = call("/tables/metadata_records/columns", token)
	if status != 200 {
		t.Fatal(result)
	}
	columns := map[string]map[string]any{}
	for _, item := range result["data"].([]any) {
		column := item.(map[string]any)
		columns[column["name"].(string)] = column
	}
	for name, key := range map[string]string{"id": "PRI", "code": "UNI", "bucket": "MUL", "region": "MUL", "enabled": ""} {
		if columns[name]["key"] != key {
			t.Fatal("字段索引不完整", name, columns[name])
		}
	}
	if !strings.Contains(fmt.Sprint(columns["code"]["type"]), "64") || !strings.Contains(fmt.Sprint(columns["amount"]["type"]), "12,2") {
		t.Fatal("字段长度或精度丢失", columns)
	}
	if columns["id"]["default"] != nil || !strings.Contains(fmt.Sprint(columns["enabled"]["default"]), "0") || columns["note"]["default"] == nil {
		t.Fatal("字段默认值丢失", columns)
	}
	if columns["enabled"]["nullable"] != false || columns["code"]["nullable"] != true {
		t.Fatal("字段非空约束丢失", columns)
	}
	if status, _ := call("/tables/does_not_exist/columns", token); status != 404 {
		t.Fatal("不存在的表未拒绝", status)
	}
	for _, path := range []string{"/tables", "/tables/metadata_records/columns"} {
		if status, _ := call(path, "invalid"); status != 401 {
			t.Fatal("未登录可查看数据库元数据", path, status)
		}
	}
	if err := a.DB.Table("metadata_labels").AutoMigrate(&struct {
		models.Base
		Name      string `gorm:"not null;size:80;comment:显示名称"`
		CreatedAt int64  `gorm:"not null;comment:登记时间"`
	}{}); err != nil {
		t.Fatal(err)
	}
	status, result = call("/code/previews?table=metadata_labels&directory=crm", token)
	if status != 200 {
		t.Fatal(status, result)
	}
	files := result["data"].(map[string]any)
	for locale, want := range map[string]string{"zh": "显示名称", "en": "Name"} {
		if locale == "zh" && a.DB.Dialector.Name() == "sqlite" {
			want = "name"
		}
		var language struct {
			Fields map[string]map[string]string `json:"fields"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(files["admin/src/locales/"+locale+"/metadata_labels.ts"].(string), "export default ")), &language); err != nil {
			t.Fatal(err)
		}
		if language.Fields["metadata_labels"]["name"] != want {
			t.Fatal("字段备注未生成中文或英文未按字段名生成", locale, language)
		}
		if locale == "zh" && a.DB.Dialector.Name() != "sqlite" && language.Fields["metadata_labels"]["created_at"] != "登记时间" {
			t.Fatal("时间字段备注被固定文案覆盖", language)
		}
	}
	if status, _ := call("/code/previews?table=metadata_labels&directory=../invalid", token); status != 422 {
		t.Fatal("目录允许路径越界", status)
	}

}
