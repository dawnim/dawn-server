package controllers

import (
	"cmp"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/dawnim/dawn-server/support/codegen"
	"reflect"
	"slices"
	"strings"

	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/app/admin/middleware"
	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/dawnim/dawn-server/support/i18n"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/prismgo/framework/route"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RegisterResourceFiles 注册导入与模板，并返回集合的CSV表示处理函数。
func RegisterResourceFiles[T any](a *admin.App, g *route.Registrar, r Resource[T], query func(*gin.Context) *gorm.DB) gin.HandlerFunc {
	name := cmp.Or(r.Name, r.Permission)
	fields := slices.DeleteFunc(slices.Clone(r.Fields), func(field string) bool {
		return slices.Contains([]string{"id", "created_at", "updated_at", "deleted_at", "created_by", "department_id"}, field)
	})
	if r.Importable {
		g.Get(r.Path+"/import-template", middleware.Require(a, r.Permission+".create"), func(c *gin.Context) {
			c.Header("Content-Disposition", "attachment; filename=import-template.csv")
			c.Header("Content-Type", "text/csv; charset=utf-8")
			writer := csv.NewWriter(c.Writer)
			if err := writer.Write(fields); err != nil {
				response.Report(c, err)
				return
			}
			writer.Flush()
		}).Name(name + ".import-template")
		g.Post(r.Path+"/imports", middleware.Require(a, r.Permission+".create"), func(c *gin.Context) {
			file, _, err := c.Request.FormFile("file")
			if err != nil {
				response.Fail(c, 422, "请选择导入文件")
				return
			}
			defer file.Close()
			rows, err := csv.NewReader(file).ReadAll()
			if err != nil || len(rows) == 0 {
				response.Fail(c, 422, "CSV格式错误")
				return
			}
			statement := &gorm.Statement{DB: a.DB}
			if err := statement.Parse(new(T)); err != nil {
				response.Report(c, err)
				return
			}
			for _, name := range rows[0] {
				if !slices.Contains(fields, name) {
					response.Fail(c, 422, "CSV包含不可导入字段: %s", name)
					return
				}
			}
			for _, field := range r.Required {
				if !slices.Contains(rows[0], field) {
					response.Fail(c, 422, "缺少必填字段: %s", field)
					return
				}
			}
			if c.Query("preview") == "true" {
				columns := make([]gin.H, len(rows[0]))
				for i, name := range rows[0] {
					field := statement.Schema.FieldsByDBName[name]
					columns[i] = gin.H{"key": name, "label": i18n.Text(c.GetString("admin.locale"), cmp.Or(strings.Split(field.Comment, ":")[0], name))}
				}
				response.Respond(c, gin.H{"columns": columns, "rows": rows[1:]})
				return
			}
			indexes, err := codegen.Indexes(a.DB.WithContext(c.Request.Context()), statement.Schema.Table)
			if err != nil {
				response.Report(c, err)
				return
			}
			count, updated := 0, 0
			err = a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
				for index, row := range rows[1:] {
					var value T
					if err := resourceDefaults(c.Request.Context(), &value, statement.Schema.Fields); err != nil {
						return err
					}
					bind := func(value *T, editing bool) error {
						for col, text := range row {
							field := statement.Schema.FieldsByDBName[rows[0][col]]
							if err := field.Set(c.Request.Context(), reflect.ValueOf(value), text); err != nil {
								return i18n.Errorf("第%d行字段%s格式错误", index+2, field.DBName)
							}
						}
						if r.Prepare != nil {
							r.Prepare(value, middleware.Actor(c), editing)
						}
						return nil
					}
					if err := bind(&value, false); err != nil {
						return err
					}
					var keys []clause.Expression
					for _, key := range indexes {
						if unique, _ := key.Unique(); !unique {
							continue
						}
						var parts []clause.Expression
						for _, column := range key.Columns() {
							field := statement.Schema.FieldsByDBName[column]
							if field == nil || field.PrimaryKey {
								parts = nil
								break
							}
							v, _ := field.ValueOf(c.Request.Context(), reflect.ValueOf(&value))
							rv := reflect.ValueOf(v)
							if !rv.IsValid() || (rv.Kind() == reflect.Pointer && rv.IsNil()) {
								parts = nil
								break
							}
							parts = append(parts, clause.Eq{Column: clause.Column{Name: column}, Value: v})
						}
						if len(parts) > 0 {
							keys = append(keys, clause.And(parts...))
						}
					}
					db := tx.Model(new(T))
					if r.Scope != nil {
						db = r.Scope(db, middleware.Actor(c))
					}
					if r.WriteScope != nil {
						db = r.WriteScope(db, middleware.Actor(c))
					}
					var existing []T
					if len(keys) > 0 {
						if err := db.Session(&gorm.Session{}).Clauses(clause.Locking{Strength: "UPDATE"}).Where(clause.Or(keys...)).Limit(2).Find(&existing).Error; err != nil {
							return err
						}
					}
					if len(existing) > 1 {
						return i18n.Errorf("第%d行匹配到多条记录，请检查唯一字段", index+2)
					}
					editing := len(existing) == 1
					if editing {
						if !middleware.Actor(c).Can(r.Permission + ".update") {
							return i18n.Errorf("第%d行记录已存在，没有更新权限", index+2)
						}
						value = existing[0]
						if err := bind(&value, true); err != nil {
							return err
						}
					}
					if err := binding.Validator.ValidateStruct(&value); err != nil {
						return i18n.Errorf("第%d行必填字段或格式错误", index+2)
					}
					if r.Validate != nil {
						if err := r.Validate(&value, middleware.Actor(c), editing); err != nil {
							return err
						}
					}
					if editing {
						if err := db.Model(&value).Select(rows[0]).Omit("created_at").Updates(&value).Error; err != nil {
							return i18n.Errorf("第%d行写入失败: %w", index+2, err)
						}
						updated++
						continue
					}
					if err := CreateResource(tx, &value, r.Fields, rows[0]); err != nil {
						if errors.Is(err, gorm.ErrDuplicatedKey) {
							return i18n.Errorf("第%d行存在重复数据或无权更新", index+2)
						}
						return i18n.Errorf("第%d行写入失败: %w", index+2, err)
					}
					count++
				}
				return nil
			})
			if err != nil {
				response.Fail(c, 422, "导入失败: %s", i18n.Message(err, c.GetString("admin.locale")))
				return
			}
			if count+updated > 0 && r.AfterWrite != nil {
				if err := r.AfterWrite(c.Request.Context()); err != nil {
					response.Report(c, err)
					return
				}
			}
			response.Respond(c, gin.H{"count": count, "updated": updated})
		}).Name(name + ".imports")
	}
	writeCSV := func(c *gin.Context) {
		var data []T
		if err := query(c).Order(response.Order(c, append(slices.Clone(r.Fields), "id", "created_at", "updated_at")...)).Order("id DESC").Find(&data).Error; err != nil {
			response.Report(c, err)
			return
		}
		var rows []map[string]any
		raw, err := json.Marshal(data)
		if err != nil {
			response.Report(c, err)
			return
		}
		if err := json.Unmarshal(raw, &rows); err != nil {
			response.Report(c, err)
			return
		}
		c.Header("Content-Disposition", "attachment; filename=export.csv")
		c.Header("Content-Type", "text/csv; charset=utf-8")
		writer := csv.NewWriter(c.Writer)
		if err := writer.Write(fields); err != nil {
			response.Report(c, err)
			return
		}
		for _, row := range rows {
			values := make([]string, len(fields))
			for i, key := range fields {
				if row[key] != nil {
					values[i] = fmt.Sprint(row[key])
					if strings.ContainsAny(values[i][:min(1, len(values[i]))], "=+-@\t\r") {
						values[i] = "'" + values[i]
					}
				}
			}
			if err := writer.Write(values); err != nil {
				response.Report(c, err)
				return
			}
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			response.Report(c, err)
		}
	}
	return writeCSV
}
