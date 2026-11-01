package controllers

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"

	"github.com/dawnim/dawn-server/support/i18n"

	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/app/admin/middleware"
	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"gorm.io/gorm"
)

// Resource 只抽取各模块重复的 CRUD, 复杂业务直接注册自己的处理函数.
type Resource[T any] struct {
	Path       string
	Permission string
	Fields     []string
	Search     []string
	Tree       bool
	Importable bool
	Scope      func(*gorm.DB, *admin.Actor) *gorm.DB
	Validate   func(*T, bool) error
	Prepare    func(*T, *admin.Actor, bool)
}

// RegisterResource 注册标准 CRUD, 字段白名单同时用于更新和排序.
func RegisterResource[T any](a *admin.App, g *gin.RouterGroup, r Resource[T]) {
	query := func(c *gin.Context) *gorm.DB {
		db := a.DB.WithContext(c.Request.Context()).Model(new(T))
		if r.Scope != nil {
			db = r.Scope(db, middleware.Actor(c))
		}
		for _, field := range r.Search {
			if value := c.Query(field); value != "" {
				db = db.Where(field+" LIKE ?", "%"+value+"%")
			}
		}
		if status := c.Query("status"); status != "" {
			for _, f := range r.Fields {
				if f == "status" {
					db = db.Where("status = ?", status)
				}
			}
		}
		return db
	}
	if r.Importable {
		g.GET(r.Path+"/template", middleware.Require(a, r.Permission+".create"), func(c *gin.Context) {
			c.Header("Content-Disposition", "attachment; filename=import-template.csv")
			c.Header("Content-Type", "text/csv; charset=utf-8")
			writer := csv.NewWriter(c.Writer)
			if err := writer.Write(r.Fields); err != nil {
				response.Report(c, err)
				return
			}
			writer.Flush()
		})
		g.POST(r.Path+"/import", middleware.Require(a, r.Permission+".create"), func(c *gin.Context) {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2<<20)
			rows, err := csv.NewReader(c.Request.Body).ReadAll()
			if err != nil || len(rows) < 2 || len(rows) > 2001 {
				response.Fail(c, 422, "CSV格式错误或超过2000条")
				return
			}
			statement := &gorm.Statement{DB: a.DB}
			if err := statement.Parse(new(T)); err != nil {
				response.Report(c, err)
				return
			}
			for _, name := range rows[0] {
				if !slices.Contains(r.Fields, name) {
					response.Fail(c, 422, "CSV包含不可导入字段: %s", name)
					return
				}
			}
			err = a.DB.Transaction(func(tx *gorm.DB) error {
				for index, row := range rows[1:] {
					var value T
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
						if err := r.Validate(&value, false); err != nil {
							return err
						}
					}
					if err := tx.Select(append(slices.Clone(r.Fields), "CreatedAt", "UpdatedAt", "DeletedAt")).Create(&value).Error; err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				response.Fail(c, 422, "导入失败: %s", i18n.Message(err, c.GetString("admin.locale")))
				return
			}
			response.Respond(c, gin.H{"count": len(rows) - 1})
		})
	}
	g.GET(r.Path, middleware.Require(a, r.Permission+".list"), func(c *gin.Context) {
		db := query(c)
		var total int64
		if err := db.Count(&total).Error; err != nil {
			response.Report(c, err)
			return
		}
		page, size := response.Page(c)
		var data []T
		if !r.Tree {
			db = db.Offset((page - 1) * size).Limit(size)
		}
		if err := db.Order(response.Order(c, append(slices.Clone(r.Fields), "id", "created_at", "updated_at")...)).Find(&data).Error; err != nil {
			response.Report(c, err)
			return
		}
		if r.Tree {
			response.Respond(c, data)
		} else {
			response.Respond(c, gin.H{"data": data, "total": total, "current_page": page, "per_page": size})
		}
	})
	g.GET(r.Path+"/export", middleware.Require(a, r.Permission+".export"), func(c *gin.Context) {
		var data []T
		if err := query(c).Limit(10000).Find(&data).Error; err != nil {
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
		if err := writer.Write(r.Fields); err != nil {
			response.Report(c, err)
			return
		}
		for _, row := range rows {
			values := make([]string, len(r.Fields))
			for i, key := range r.Fields {
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
	})
	g.GET(r.Path+"/:id", middleware.Require(a, r.Permission+".list"), func(c *gin.Context) {
		id := response.ID(c)
		if c.IsAborted() {
			return
		}
		var value T
		if err := query(c).First(&value, id).Error; err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, value)
	})
	for _, editing := range []bool{false, true} {
		method, path, permission := http.MethodPost, r.Path, r.Permission+".create"
		if editing {
			method, path, permission = http.MethodPut, r.Path+"/:id", r.Permission+".update"
		}
		g.Handle(method, path, middleware.Require(a, permission), func(c *gin.Context) {
			var value T
			if err := c.ShouldBindJSON(&value); err != nil {
				response.Fail(c, 422, "请检查必填字段和字段格式")
				return
			}
			var id uint
			var old T
			if editing {
				id = response.ID(c)
				if c.IsAborted() {
					return
				}
				if err := query(c).First(&old, id).Error; err != nil {
					response.Report(c, err)
					return
				}
			}
			reflect.ValueOf(&value).Elem().FieldByName("Base").Set(reflect.ValueOf(models.Base{ID: id}))
			if r.Prepare != nil {
				r.Prepare(&value, middleware.Actor(c), editing)
			}
			if editing {
				for _, field := range []string{"CreatedBy", "DepartmentID"} {
					oldField := reflect.ValueOf(&value).Elem().FieldByName(field)
					if oldField.IsValid() {
						oldField.Set(reflect.ValueOf(old).FieldByName(field))
					}
				}
			}
			if role, ok := any(&value).(*models.Role); ok && !middleware.Actor(c).User.Super {
				if role.Level <= middleware.Actor(c).Level() || role.DataScope != models.ScopeSelf {
					response.Fail(c, 403, "不能设置更高级别或扩展数据范围")
					return
				}
			}
			if r.Validate != nil {
				if err := r.Validate(&value, editing); err != nil {
					response.Report(c, err, 422)
					return
				}
			}
			var err error
			if editing {
				err = query(c).Where("id = ?", id).Select(r.Fields).Updates(&value).Error
			} else {
				err = a.DB.Select(append(slices.Clone(r.Fields), "CreatedAt", "UpdatedAt", "DeletedAt")).Create(&value).Error
			}
			if err != nil {
				response.Report(c, err)
				return
			}
			response.Respond(c, value)
		})
	}
	g.DELETE(r.Path+"/:id", middleware.Require(a, r.Permission+".delete"), func(c *gin.Context) {
		id := response.ID(c)
		if c.IsAborted() {
			return
		}
		result := query(c).Delete(new(T), id)
		if result.Error != nil {
			response.Report(c, result.Error)
			return
		}
		if result.RowsAffected == 0 {
			response.Fail(c, 404, "记录不存在")
			return
		}
		response.Respond(c, nil)
	})
}
