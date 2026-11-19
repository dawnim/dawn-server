package migrations

import (
	"context"
	"slices"
	"strings"

	"github.com/dawnim/dawn-server/database/schema"
	"github.com/dawnim/dawn-server/support/i18n"
	"gorm.io/gorm"
)

// Check checks required built-in tables and columns without executing DDL.
func Check(ctx context.Context, db *gorm.DB) error { return CheckDefinition(ctx, db, Definition()) }

// CheckDefinition is the inexpensive startup readiness check for the current target.
func CheckDefinition(ctx context.Context, db *gorm.DB, definition *schema.Definition) error {
	db = db.WithContext(ctx)
	connection, err := db.DB()
	if err != nil {
		return err
	}
	if err = connection.PingContext(ctx); err != nil {
		return err
	}
	missing := []string{}
	for name, table := range definition.Tables {
		if !db.Migrator().HasTable(name) {
			missing = append(missing, name)
			continue
		}
		stmt := &gorm.Statement{DB: db}
		if err = stmt.ParseWithSpecialTableName(table.Model, name); err != nil {
			return err
		}
		columns, err := db.Migrator().ColumnTypes(name)
		if err != nil {
			return err
		}
		present := map[string]bool{}
		for _, column := range columns {
			present[column.Name()] = true
		}
		for _, field := range stmt.Schema.DBNames {
			if !stmt.Schema.FieldsByDBName[field].IgnoreMigration && !present[field] {
				missing = append(missing, name+"."+field)
			}
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return i18n.Errorf("数据库结构尚未就绪，缺少%s；请执行安装或未运行的迁移，已执行迁移内的改动请执行dawn:schema check和dawn:schema sync", strings.Join(missing, ", "))
	}
	return nil
}
