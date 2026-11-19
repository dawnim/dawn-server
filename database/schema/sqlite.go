package schema

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dawnim/dawn-server/support/i18n"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// ddlLog captures GORM's CREATE TABLE output instead of implementing a SQL compiler.
type ddlLog struct {
	logger.Interface
	sql string
}

func (l *ddlLog) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	q, _ := fc()
	if strings.HasPrefix(q, "CREATE TABLE ") {
		l.sql = q
	}
}

func (d *Definition) applySQLite(db *gorm.DB, p *Plan) error {
	names, _, err := d.ordered(db)
	if err != nil {
		return err
	}
	return db.Connection(func(conn *gorm.DB) (result error) {
		var foreignKeys int
		if err := conn.Raw("PRAGMA foreign_keys").Scan(&foreignKeys).Error; err != nil {
			return err
		}
		if err := conn.Exec("PRAGMA foreign_keys=OFF").Error; err != nil {
			return err
		}
		defer func() {
			result = errors.Join(result, conn.WithContext(context.WithoutCancel(db.Statement.Context)).Exec(fmt.Sprintf("PRAGMA foreign_keys=%d", foreignKeys)).Error)
		}()
		return conn.Transaction(func(tx *gorm.DB) error {
			for _, name := range names {
				changes := []Change{}
				for _, c := range p.Changes {
					if c.Table == name {
						changes = append(changes, c)
					}
				}
				if len(changes) == 0 {
					continue
				}
				table := d.Tables[name]
				target := tx.Table(name)
				rebuild := false
				for _, c := range changes {
					if c.Action == "alter_column" || c.Action == "drop_column" || c.Action == "add_column" || strings.HasSuffix(c.Action, "_constraint") {
						rebuild = true
					}
				}
				if changes[0].Action == "create_table" {
					if err := target.Migrator().CreateTable(table.Model); err != nil {
						return err
					}
					continue
				}
				if rebuild {
					if err := rebuildSQLite(tx, name, table); err != nil {
						return i18n.Errorf("结构同步失败%s.%s（%s）：%w", name, "", "rebuild_table", err)
					}
				} else {
					for _, c := range changes {
						var err error
						switch c.Action {
						case "drop_index":
							err = target.Migrator().DropIndex(table.Model, c.Name)
						case "add_index":
							err = target.Migrator().CreateIndex(table.Model, c.Name)
						}
						if err != nil {
							return err
						}
					}
				}
			}
			for _, c := range p.Changes {
				if c.Action == "drop_table" {
					if err := tx.Migrator().DropTable(c.Table); err != nil {
						return err
					}
				}
			}
			// Foreign-key checking was disabled only for DDL; invalid retained data rolls back.
			var violations []struct {
				Table  string
				Rowid  *int64
				Parent string
				FKID   int
			}
			if err := tx.Raw("PRAGMA foreign_key_check").Scan(&violations).Error; err != nil {
				return err
			}
			if len(violations) > 0 {
				return i18n.Errorf("结构同步后存在外键冲突：%s", violations[0].Table)
			}
			return nil
		})
	})
}

func rebuildSQLite(db *gorm.DB, name string, table Table) error {
	var triggers []string
	if err := db.Raw("SELECT sql FROM sqlite_master WHERE type='trigger' AND tbl_name=? ORDER BY name", name).Scan(&triggers).Error; err != nil {
		return err
	}
	temp := "__dawn_sync_" + rand.Text()[:12]
	captured := &ddlLog{Interface: logger.Default}
	dry := db.Session(&gorm.Session{DryRun: true, Logger: captured}).Table(temp)
	if err := dry.Migrator().CreateTable(table.Model); err != nil {
		return err
	}
	if captured.sql == "" {
		return i18n.Errorf("无法生成目标建表语句：%s", name)
	}
	if err := db.Exec(captured.sql).Error; err != nil {
		return err
	}
	var oldCols, newCols []string
	if err := db.Raw("SELECT name FROM pragma_table_info(?)", name).Scan(&oldCols).Error; err != nil {
		return err
	}
	if err := db.Raw("SELECT name FROM pragma_table_xinfo(?) WHERE hidden=0", temp).Scan(&newCols).Error; err != nil {
		return err
	}
	common := []string{}
	for _, col := range newCols {
		for _, old := range oldCols {
			if col == old {
				common = append(common, db.Statement.Quote(col))
				break
			}
		}
	}
	if len(common) > 0 {
		if err := db.Exec("INSERT INTO ? ("+strings.Join(common, ",")+") SELECT "+strings.Join(common, ",")+" FROM ?", clause.Table{Name: temp}, clause.Table{Name: name}).Error; err != nil {
			return err
		}
	} else {
		var count int64
		if err := db.Table(name).Count(&count).Error; err != nil {
			return err
		}
		for range count {
			if err := db.Exec("INSERT INTO ? DEFAULT VALUES", clause.Table{Name: temp}).Error; err != nil {
				return err
			}
		}
	}
	// Preserve the AUTOINCREMENT high-water mark even if the largest row was deleted.
	var sequence int64
	if db.Migrator().HasTable("sqlite_sequence") {
		if err := db.Raw("SELECT COALESCE(MAX(seq),0) FROM sqlite_sequence WHERE name=?", name).Scan(&sequence).Error; err != nil {
			return err
		}
	}
	if err := db.Migrator().DropTable(name); err != nil {
		return err
	}
	if err := db.Migrator().RenameTable(temp, name); err != nil {
		return err
	}
	if sequence > 0 && strings.Contains(strings.ToUpper(captured.sql), "AUTOINCREMENT") {
		updated := db.Exec("UPDATE sqlite_sequence SET seq=MAX(seq,?) WHERE name=?", sequence, name)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected == 0 {
			if err := db.Exec("INSERT INTO sqlite_sequence(name,seq) VALUES (?,?)", name, sequence).Error; err != nil {
				return err
			}
		}
	}
	s := &gorm.Statement{DB: db}
	if err := s.ParseWithSpecialTableName(table.Model, name); err != nil {
		return err
	}
	for _, idx := range s.Schema.ParseIndexes() {
		if err := db.Table(name).Migrator().CreateIndex(table.Model, idx.Name); err != nil {
			return err
		}
	}
	for _, trigger := range triggers {
		if err := db.Exec(trigger).Error; err != nil {
			return err
		}
	}
	return nil
}
