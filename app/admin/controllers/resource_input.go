package controllers

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

// BindResourceInput 将实际提交的可写字段绑定到新记录或已读取的记录。
// 返回实际提交的数据库字段名；PATCH保留缺省字段，PUT重置全部可写字段。
func BindResourceInput(db *gorm.DB, c *gin.Context, value any, fields []string) []string {
	var input map[string]json.RawMessage
	if c.ShouldBindJSON(&input) != nil || input == nil || (len(input) == 0 && c.Request.Method != "PUT") {
		response.Fail(c, 422, "参数格式错误")
		return nil
	}
	statement := &gorm.Statement{DB: db}
	if err := statement.Parse(value); err != nil {
		response.Report(c, err)
		return nil
	}
	if c.Request.Method == "POST" {
		if err := resourceDefaults(c.Request.Context(), value, statement.Schema.Fields); err != nil {
			response.Fail(c, 422, "参数格式错误")
			return nil
		}
	}
	allowed := map[string]*schema.Field{}
	var replaceFields []*schema.Field
	for _, field := range statement.Schema.Fields {
		name := strings.Split(field.StructField.Tag.Get("json"), ",")[0]
		if slices.Contains(fields, field.DBName) && name != "-" {
			allowed[name] = field
			if c.Request.Method == "PUT" {
				if err := field.Set(c.Request.Context(), reflect.ValueOf(value), reflect.Zero(field.FieldType).Interface()); err != nil {
					response.Fail(c, 422, "参数格式错误")
					return nil
				}
				replaceFields = append(replaceFields, field)
			}
		}
	}
	if err := resourceDefaults(c.Request.Context(), value, replaceFields); err != nil {
		response.Fail(c, 422, "参数格式错误")
		return nil
	}
	selected := make([]string, 0, len(input))
	for name, raw := range input {
		field, ok := allowed[name]
		if !ok || (string(raw) == "null" && (field.NotNull || (field.FieldType.Kind() != reflect.Pointer && field.FieldType.Kind() != reflect.Slice))) {
			response.Fail(c, 422, "字段不可写或格式错误: %s", name)
			return nil
		}
		selected = append(selected, field.DBName)
	}
	raw, _ := json.Marshal(input)
	if json.Unmarshal(raw, value) != nil || binding.Validator.ValidateStruct(value) != nil {
		response.Fail(c, 422, "请检查必填字段和字段格式")
		return nil
	}
	return selected
}

// CreateResource 使用原生map写入保留显式零值，让未提交的字段使用数据库默认值。
func CreateResource[T any](db *gorm.DB, row *T, fields, submitted []string) error {
	statement := &gorm.Statement{DB: db}
	if err := statement.Parse(row); err != nil {
		return err
	}
	values := map[string]any{}
	for _, field := range statement.Schema.Fields {
		if field.AutoCreateTime > 0 || field.AutoUpdateTime > 0 {
			values[field.DBName] = time.Now().UnixMilli()
		} else if slices.Contains(fields, field.DBName) {
			value, zero := field.ValueOf(db.Statement.Context, reflect.ValueOf(row))
			if !zero || !field.HasDefaultValue || slices.Contains(submitted, field.DBName) {
				values[field.DBName] = value
			}
		}
	}
	primary := statement.Schema.PrioritizedPrimaryField.DBName
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(row).Clauses(clause.Returning{Columns: []clause.Column{{Name: primary}}}).Create(values).Error; err != nil {
			return err
		}
		return tx.Where(primary+" = ?", values[primary]).First(row).Error
	})
}

// resourceDefaults 在JSON与CSV校验前使用ORM声明的默认值，随后由显式输入覆盖。
func resourceDefaults(ctx context.Context, value any, fields []*schema.Field) error {
	for _, field := range fields {
		if field.Serializer != nil && field.HasDefaultValue {
			if err := field.Serializer.Scan(ctx, field, reflect.ValueOf(value), strings.Trim(field.DefaultValue, "'")); err != nil {
				return err
			}
		} else if field.DefaultValueInterface != nil {
			if err := field.Set(ctx, reflect.ValueOf(value), field.DefaultValueInterface); err != nil {
				return err
			}
		}
	}
	return nil
}
