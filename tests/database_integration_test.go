package tests

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/database/migrations"
)

// TestDatabaseMatrix 仅连接明确指定的独立测试库, CI分别提供各类数据库.
func TestDatabaseMatrix(t *testing.T) {
	driver, dsn := os.Getenv("DAWN_TEST_DRIVER"), os.Getenv("DAWN_TEST_DSN")
	if driver == "" || dsn == "" {
		t.Skip("未指定DAWN_TEST_DRIVER/DAWN_TEST_DSN")
	}
	r := miniredis.RunT(t)
	a, err := newTestApp(testOptions{DBDriver: driver, DBDSN: dsn, Storage: t.TempDir(), RedisURL: "redis://" + r.Addr()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := migrations.Run(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	tables, err := a.DB.Migrator().GetTables()
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		if !strings.HasPrefix(table, "admin_") {
			continue
		}
		var comment string
		switch a.DB.Dialector.Name() {
		case "mysql":
			err = a.DB.Raw("SELECT table_comment FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ?", table).Scan(&comment).Error
		case "postgres":
			err = a.DB.Raw("SELECT obj_description(to_regclass(?), 'pg_class')", table).Scan(&comment).Error
		}
		if err != nil || comment == "" {
			t.Fatalf("%s 缺少表注释: %v", table, err)
		}
		columns, err := a.DB.Migrator().ColumnTypes(table)
		if err != nil {
			t.Fatal(err)
		}
		for _, column := range columns {
			if !slices.Contains([]string{"id", "created_at", "updated_at", "deleted_at"}, column.Name()) {
				if comment, ok := column.Comment(); !ok || comment == "" {
					t.Errorf("%s.%s 缺少字段注释", table, column.Name())
				}
			}
			if (strings.HasSuffix(column.Name(), "_at") || column.Name() == "login_time") && !slices.Contains([]string{"bigint", "integer", "int8", "int"}, strings.ToLower(column.DatabaseTypeName())) {
				t.Fatalf("%s.%s 时间字段类型不是整数: %s", table, column.Name(), column.DatabaseTypeName())
			}
		}
	}
	if err := migrations.Run(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	role := models.Role{Name: "test", Code: fmt.Sprintf("test-role-%d", time.Now().UnixNano()), Level: 100, Status: 1, DataScope: models.ScopeSelf}
	if err := a.DB.Create(&role).Error; err != nil {
		t.Fatal(err)
	}
	if role.CreatedAt < time.Now().Add(-time.Minute).UnixMilli() {
		t.Fatal("创建时间不是毫秒整数")
	}
	if err := a.DB.Delete(&role).Error; err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := a.DB.Model(&models.Role{}).Where("id = ?", role.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("软删除失败: %d %v", count, err)
	}
}
