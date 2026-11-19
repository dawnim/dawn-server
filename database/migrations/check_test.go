package migrations

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestCheckSchemaIsReadOnly(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "schema.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	ctx := context.Background()
	if err = Check(ctx, db); err == nil || db.Migrator().HasTable("support_sessions") {
		t.Fatal("empty database was modified", err)
	}
	if err = Up(db); err != nil {
		t.Fatal(err)
	}
	if err = Check(ctx, db); err != nil {
		t.Fatal(err)
	}
	row := SupportSession{ConversationID: 7, Round: 1, Comment: "preserve"}
	if err = db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Migrator().DropColumn(&SupportSession{}, "OfflineSince"); err != nil {
		t.Fatal(err)
	}
	if err = Check(ctx, db); err == nil || !strings.Contains(err.Error(), "support_sessions.offline_since") {
		t.Fatal("missing column not reported", err)
	}
	if db.Migrator().HasColumn(&SupportSession{}, "OfflineSince") {
		t.Fatal("check repaired schema implicitly")
	}
	if err = db.Migrator().AddColumn(&SupportSession{}, "OfflineSince"); err != nil {
		t.Fatal(err)
	}
	if err = Check(ctx, db); err != nil {
		t.Fatal(err)
	}
	var saved SupportSession
	if err = db.First(&saved, row.ID).Error; err != nil || saved.Comment != "preserve" {
		t.Fatal(saved, err)
	}
}
