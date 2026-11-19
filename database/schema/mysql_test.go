package schema_test

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/dawnim/dawn-server/database/migrations"
	"github.com/dawnim/dawn-server/database/schema"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type indexedChild struct {
	ID       uint   `gorm:"primaryKey"`
	ParentID uint   `gorm:"index:idx_children_parent"`
	Parent   parent `gorm:"foreignKey:ParentID;constraint:OnDelete:CASCADE"`
}

func TestMySQLSchemaSync(t *testing.T) {
	if driver := os.Getenv("DAWN_TEST_DRIVER"); (driver != "mysql" && driver != "mariadb") || os.Getenv("DAWN_TEST_DSN") == "" {
		t.Skip("需要MySQL或MariaDB专用空库")
	}
	db, err := gorm.Open(mysql.Open(os.Getenv("DAWN_TEST_DSN")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	connection, _ := db.DB()
	defer connection.Close()
	tables, err := db.Migrator().GetTables()
	if err != nil || len(tables) > 0 {
		t.Fatal("需要专用空库", tables, err)
	}
	ctx := context.Background()
	if err := migrations.Up(db); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { migrations.Down(db) })
	definition := migrations.Definition()
	check := func() *schema.Plan {
		t.Helper()
		plan, err := definition.Check(ctx, db)
		if err != nil {
			t.Fatal(err)
		}
		return plan
	}
	if plan := check(); len(plan.Changes) > 0 {
		t.Fatalf("新安装应无差异: %+v", plan.Changes)
	}
	// Model association renames may leave an old supporting index behind.
	for _, query := range []string{
		"ALTER TABLE admin_attachments DROP FOREIGN KEY fk_admin_attachments_file",
		"ALTER TABLE admin_attachments RENAME INDEX fk_admin_attachments_file TO legacy_attachment_file",
		"ALTER TABLE admin_attachments ADD CONSTRAINT legacy_attachment_fk FOREIGN KEY(file_id) REFERENCES admin_files(id)",
	} {
		if err := db.Exec(query).Error; err != nil {
			t.Fatal(err)
		}
	}
	plan := check()
	if slices.ContainsFunc(plan.Changes, func(c schema.Change) bool { return c.Action == "drop_index" && c.Name == "legacy_attachment_file" }) {
		t.Fatal("外键必需索引不应删除", plan)
	}
	if err := definition.Apply(ctx, db, plan); err != nil {
		t.Fatal(err)
	}
	if plan := check(); len(plan.Changes) > 0 {
		t.Fatal("同步后仍有差异", plan)
	}
	definition.Tables["parents"] = schema.Table{Model: &parent{}}
	definition.Tables["children"] = schema.Table{Model: &child{}}
	if err := definition.Apply(ctx, db, check()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Migrator().DropTable("children", "parents") })
	if err := db.Exec("INSERT INTO parents(id,name) VALUES(1,'retained')").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO children(parent_id) VALUES(1)").Error; err != nil {
		t.Fatal(err)
	}
	// Replace the implicit FK index with an explicitly named target index.
	definition.Tables["children"] = schema.Table{Model: &indexedChild{}}
	if err := definition.Apply(ctx, db, check()); err != nil {
		t.Fatal(err)
	}
	if plan := check(); len(plan.Changes) > 0 {
		t.Fatal("替换索引后不收敛", plan)
	}
	if err := db.Exec("INSERT INTO children(parent_id) VALUES(999)").Error; err == nil {
		t.Fatal("外键约束丢失")
	}
	var total int64
	if err := db.Table("children").Count(&total).Error; err != nil || total != 1 {
		t.Fatal("原有数据未保留", total, err)
	}
}
