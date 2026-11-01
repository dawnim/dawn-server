package controllers

import (
	"cmp"
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dawnim/dawn-server/support/i18n"

	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/app/admin/middleware"
	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/gin-gonic/gin"
	"github.com/prismgo/framework/config"
	"github.com/prismgo/framework/encryption"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (a *Controller) SettingRoutes(g *gin.RouterGroup) {
	// 公开站点展示与时钟信息，业务参数和凭证由授权接口读取。
	g.GET("/site", func(c *gin.Context) {
		values := map[string]json.RawMessage{}
		for _, key := range []string{"title", "logo", "copyright", "description", "keywords", "record_number"} {
			value, err := a.Setting(c.Request.Context(), "site."+key)
			if err != nil {
				response.Report(c, err)
				return
			}
			values[key] = value
		}
		var logo string
		if err := json.Unmarshal(values["logo"], &logo); err != nil {
			response.Report(c, err)
			return
		}
		if id, ok := strings.CutPrefix(logo, a.Options.AdminPrefix+"/attachments/"); ok {
			if id, ok = strings.CutSuffix(id, "/content"); ok {
				values["logo"], _ = json.Marshal(a.Options.AdminPrefix + "/site/logo?file=" + id)
			}
		}
		values["timezone"], _ = json.Marshal(config.GetString("app.timezone", "UTC"))
		values["server_time"], _ = json.Marshal(time.Now().UnixMilli())
		response.Respond(c, values)
	})
	g.GET("/site/logo", func(c *gin.Context) {
		raw, err := a.Setting(c.Request.Context(), "site.logo")
		if err != nil {
			response.Report(c, err)
			return
		}
		var logo string
		if err := json.Unmarshal(raw, &logo); err != nil {
			response.Report(c, err)
			return
		}
		id, prefix := strings.CutPrefix(logo, a.Options.AdminPrefix+"/attachments/")
		id, suffix := strings.CutSuffix(id, "/content")
		n, err := strconv.ParseUint(id, 10, 64)
		if !prefix || !suffix || err != nil {
			c.Status(404)
			return
		}
		var row models.Attachment
		if err := a.DB.Preload("File").First(&row, n).Error; err != nil {
			response.Report(c, err)
			return
		}
		if !slices.Contains([]string{"image/png", "image/jpeg", "image/gif", "image/webp"}, row.File.MIME) {
			c.Status(404)
			return
		}
		a.serveAttachment(c, row)
	})
	g.GET("/system/settings", middleware.Require(a.App, "dawn.settings.list"), func(c *gin.Context) {
		rows := []gin.H{}
		definitions := slices.SortedFunc(maps.Values(a.SettingDefinitions), func(x, y admin.SettingDefinition) int {
			return cmp.Or(cmp.Compare(x.Sort, y.Sort), cmp.Compare(x.Key, y.Key))
		})
		for _, d := range definitions {
			if group := c.Query("group"); group != "" && group != d.Group {
				continue
			}
			var value any
			if d.Secret {
				raw, err := a.Setting(c.Request.Context(), d.Key)
				if err != nil {
					response.Report(c, err)
					return
				}
				var secret string
				if err := json.Unmarshal(raw, &secret); err != nil {
					response.Report(c, err)
					return
				}
				if secret != "" {
					value = "********"
				} else {
					value = ""
				}
			} else {
				raw, err := a.Setting(c.Request.Context(), d.Key)
				if err != nil {
					response.Report(c, err)
					return
				}
				value = json.RawMessage(raw)
			}
			options := slices.Clone(d.Options)
			for i := range options {
				options[i].Label = i18n.Text(c.GetString("admin.locale"), options[i].Label)
			}
			rows = append(rows, gin.H{"input": d.Input, "options": options, "remark": i18n.Text(c.GetString("admin.locale"), d.Remark), "sort": d.Sort, "when": d.When, "key": d.Key, "title": i18n.Text(c.GetString("admin.locale"), d.Title), "group_key": d.Group, "group": i18n.Text(c.GetString("admin.locale"), d.Group), "kind": d.Kind, "secret": d.Secret, "value": value})
		}
		response.Respond(c, rows)
	})
	g.PUT("/system/settings", middleware.Require(a.App, "dawn.settings.update"), func(c *gin.Context) {
		var input map[string]json.RawMessage
		if c.ShouldBindJSON(&input) != nil {
			response.Fail(c, 422, "配置格式错误")
			return
		}
		groups := map[string]map[string]json.RawMessage{}
		for key, value := range input {
			d, exists := a.SettingDefinitions[key]
			if !exists {
				response.Fail(c, 422, "配置未注册: %s", key)
				return
			}
			var decoded any
			if json.Unmarshal(value, &decoded) != nil {
				response.Fail(c, 422, "配置值不是有效JSON")
				return
			}
			valid := false
			switch d.Kind {
			case "string":
				_, valid = decoded.(string)
			case "number":
				_, valid = decoded.(float64)
			case "boolean":
				_, valid = decoded.(bool)
			case "json":
				valid = true
			}
			if len(d.Options) > 0 && !slices.ContainsFunc(d.Options, func(o admin.SettingOption) bool { return reflect.DeepEqual(o.Value, decoded) }) {
				valid = false
			}
			if key == "upload.max_size" {
				n, ok := decoded.(float64)
				valid = ok && n > 0
			}
			if !valid {
				response.Fail(c, 422, "配置值类型错误: %s", key)
				return
			}
			raw := string(value)
			if d.Secret {
				if decoded == "" || decoded == "********" {
					continue
				}
				encrypted, err := encryption.EncryptString(c.Request.Context(), raw)
				if err != nil {
					response.Report(c, err)
					return
				}
				raw = encrypted
			}
			group, field, _ := strings.Cut(key, ".")
			if groups[group] == nil {
				groups[group] = map[string]json.RawMessage{}
			}
			if d.Secret {
				value, _ = json.Marshal(raw)
			}
			groups[group][field] = value
		}
		rows := []models.Setting{}
		err := a.DB.WithContext(c.Request.Context()).Transaction(func(db *gorm.DB) error {
			for group, changes := range groups {
				var row models.Setting
				if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).Where(map[string]any{"key": group}).First(&row).Error; err != nil {
					return err
				}
				values := map[string]json.RawMessage{}
				if err := json.Unmarshal([]byte(row.Value), &values); err != nil {
					return err
				}
				maps.Copy(values, changes)
				raw, err := json.Marshal(values)
				if err != nil {
					return err
				}
				row.Value, row.UpdatedBy, row.Revision = string(raw), middleware.Actor(c).User.ID, row.Revision+1
				if err := db.Save(&row).Error; err != nil {
					return err
				}
				rows = append(rows, row)
			}
			return nil
		})
		if err != nil {
			response.Report(c, err)
			return
		}
		for _, row := range rows {
			if err := a.CacheSetting(c.Request.Context(), row); err != nil {
				response.Report(c, err)
				return
			}
		}
		response.Respond(c, nil)
	})
}
