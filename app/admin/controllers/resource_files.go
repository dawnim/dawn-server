package controllers

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
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
)

// RegisterResourceFiles 注册导入与模板，并返回集合的CSV表示处理函数。
func RegisterResourceFiles[T any](a *admin.App, g *route.Registrar, r Resource[T], query func(*gin.Context) *gorm.DB) gin.HandlerFunc {
	fields := slices.DeleteFunc(slices.Clone(r.Fields), func(field string) bool { return field == "created_by" || field == "department_id" })
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
		})
		g.Post(r.Path+"/imports", middleware.Require(a, r.Permission+".create"), func(c *gin.Context) {
			file, _, err := c.Request.FormFile("file")
			if err != nil {
				response.Fail(c, 422, "请选择导入文件")
				return
			}
			defer file.Close()
			rows, err := csv.NewReader(file).ReadAll()
			if err != nil || len(rows) < 2 {
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
			err = a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
				for index, row := range rows[1:] {
					var value T
					if err := resourceDefaults(c.Request.Context(), &value, statement.Schema.Fields); err != nil {
						return err
					}
					for col, text := range row {
						field := statement.Schema.FieldsByDBName[rows[0][col]]
						if field == nil {
							return i18n.Errorf("字段不存在")
						}
						if err := field.Set(c.Request.Context(), reflect.ValueOf(&value), text); err != nil {
							return i18n.Errorf("第%d行字段%s格式错误", index+2, field.DBName)
						}
					}
					if r.Prepare != nil {
						r.Prepare(&value, middleware.Actor(c), false)
					}
					if err := binding.Validator.ValidateStruct(&value); err != nil {
						return i18n.Errorf("第%d行必填字段或格式错误", index+2)
					}
					if r.Validate != nil {
						if err := r.Validate(&value, middleware.Actor(c), false); err != nil {
							return err
						}
					}
					if err := CreateResource(tx, &value, r.Fields, rows[0]); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				response.Fail(c, 422, "导入失败: %s", i18n.Message(err, c.GetString("admin.locale")))
				return
			}
			if r.AfterWrite != nil {
				if err := r.AfterWrite(c.Request.Context()); err != nil {
					response.Report(c, err)
					return
				}
			}
			response.Respond(c, gin.H{"count": len(rows) - 1})
		})
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
