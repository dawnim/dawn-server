package controllers

import (
	"database/sql"
	"fmt"
	"go/format"
	"strconv"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/migrator"
)

func columnGoType(column gorm.ColumnType) string {
	kind, sqlType := "string", strings.ToUpper(column.DatabaseTypeName())
	switch {
	case strings.Contains(sqlType, "INT"):
		kind = "int64"
	case strings.Contains(sqlType, "BOOL"):
		kind = "bool"
	case strings.Contains(sqlType, "FLOAT"), strings.Contains(sqlType, "DOUBLE"), strings.Contains(sqlType, "REAL"):
		kind = "float64"
	}
	if nullable, _ := column.Nullable(); nullable {
		kind = "*" + kind
	}
	return kind
}

// generateMigration 将当前结构固定在迁移中，后续修改业务模型不会改变这份建表记录。
func generateMigration(db *gorm.DB, table string, columns []gorm.ColumnType) ([]byte, error) {
	indexes, err := db.Migrator().GetIndexes(table)
	if err != nil {
		return nil, err
	}
	// SQLite 驱动不返回 UNIQUE 约束产生的索引，补取它们的列顺序。
	if db.Dialector.Name() == "sqlite" {
		var names []string
		if err := db.Raw("SELECT name FROM pragma_index_list(?) WHERE origin = 'u'", table).Scan(&names).Error; err != nil {
			return nil, err
		}
		for _, name := range names {
			var fields []string
			if err := db.Raw("SELECT name FROM pragma_index_info(?) ORDER BY seqno", name).Scan(&fields).Error; err != nil {
				return nil, err
			}
			indexes = append(indexes, &migrator.Index{NameValue: "idx_" + table + "_" + strings.Join(fields, "_"), ColumnList: fields, UniqueValue: sql.NullBool{Bool: true, Valid: true}})
		}
	}
	indexTags := map[string][]string{}
	for _, index := range indexes {
		if primary, _ := index.PrimaryKey(); primary {
			continue
		}
		kind := "index"
		if unique, _ := index.Unique(); unique {
			kind = "uniqueIndex"
		}
		for i, field := range index.Columns() {
			indexTags[field] = append(indexTags[field], fmt.Sprintf("%s:%s,priority:%d", kind, index.Name(), i+1))
		}
	}
	var fields strings.Builder
	for _, column := range columns {
		fullType, _ := column.ColumnType()
		if fullType == "" {
			fullType = column.DatabaseTypeName()
		}
		tags := []string{"column:" + column.Name(), "type:" + fullType}
		if nullable, _ := column.Nullable(); !nullable {
			tags = append(tags, "not null")
		}
		primary, _ := column.PrimaryKey()
		increment, _ := column.AutoIncrement()
		if primary {
			tags = append(tags, "primaryKey", "autoIncrement:"+strconv.FormatBool(increment))
		}
		if value, ok := column.DefaultValue(); ok && !increment {
			tags = append(tags, "default:"+strings.ReplaceAll(value, ";", `\;`))
		}
		if comment, _ := column.Comment(); comment != "" {
			tags = append(tags, "comment:"+strings.ReplaceAll(comment, ";", `\;`))
		}
		if unique, _ := column.Unique(); unique && len(indexTags[column.Name()]) == 0 {
			tags = append(tags, "unique")
		}
		tags = append(tags, indexTags[column.Name()]...)
		tag := "gorm:" + strconv.Quote(strings.Join(tags, ";"))
		fmt.Fprintf(&fields, "%s %s %s\n", goName(column.Name()), columnGoType(column), strconv.Quote(tag))
	}
	commentSQL, tableComment := "", ""
	switch db.Dialector.Name() {
	case "mysql":
		info, err := db.Migrator().TableType(table)
		if err != nil {
			return nil, err
		}
		tableComment, _ = info.Comment()
	case "postgres":
		if err := db.Raw("SELECT COALESCE(obj_description(to_regclass(?)), '')", table).Scan(&tableComment).Error; err != nil {
			return nil, err
		}
	}
	if tableComment != "" {
		var quoted strings.Builder
		db.Dialector.QuoteTo(&quoted, table)
		if db.Dialector.Name() == "mysql" {
			commentSQL = "ALTER TABLE " + quoted.String() + " COMMENT = " + db.Dialector.Explain("?", tableComment)
		} else {
			commentSQL = "COMMENT ON TABLE " + quoted.String() + " IS " + db.Dialector.Explain("$1", tableComment)
		}
	}
	comment := "return nil"
	if commentSQL != "" {
		comment = "return db.Exec(" + strconv.Quote(commentSQL) + ").Error"
	}
	name := "migration" + goName(table)
	source := fmt.Sprintf(`package migrations
import("github.com/prismgo/framework/database";"gorm.io/gorm")
// 结构快照来自生成时的数据库，独立于业务模型。
type %s struct {%s}
func(%s) TableName()string{return %s}
func init(){database.RegisterMigration(func(db *gorm.DB)error{
 if db.Migrator().HasTable(&%s{}){return nil}
 if err:=db.Migrator().CreateTable(&%s{});err!=nil{return err}
 %s
},func(db *gorm.DB)error{return db.Migrator().DropTable(&%s{})})}
`, name, fields.String(), name, strconv.Quote(table), name, name, comment, name)
	return format.Source([]byte(source))
}
