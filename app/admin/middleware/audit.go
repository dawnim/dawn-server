package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/support/geoip"
	"github.com/gin-gonic/gin"
	"github.com/prismgo/framework/logger"
	"gorm.io/gorm"
)

const auditLimit = 16 << 10

type auditWriter struct {
	gin.ResponseWriter
	body      bytes.Buffer
	truncated bool
}

func (w *auditWriter) Write(data []byte) (int, error) {
	if w.body.Len()+len(data) <= auditLimit && !w.truncated {
		w.body.Write(data)
	} else {
		w.truncated = true
	}
	return w.ResponseWriter.Write(data)
}
func (w *auditWriter) WriteString(data string) (int, error) { return w.Write([]byte(data)) }

// auditJSON 保留大小限制内的原始JSON正文。
func auditJSON(data []byte) string {
	if len(data) > auditLimit || !json.Valid(data) {
		return ""
	}
	return string(data)
}

func RecordAudit(db *gorm.DB, c *gin.Context, kind string, status int, message string) {
	entry := models.Log{Sender: c.GetString("admin.mail.sender"), Recipient: c.GetString("admin.mail.recipient"), Kind: kind, Method: c.Request.Method, Path: c.Request.URL.Path, IP: c.ClientIP(), Status: status, Message: message, Duration: time.Since(c.GetTime("dawn.started")).Milliseconds(), RequestData: c.GetString("admin.audit.request"), ResponseData: c.GetString("admin.audit.response")}
	entry.CountryCode, entry.Location, _ = geoip.Lookup(entry.IP)
	if v, ok := c.Get("dawn.actor"); ok {
		entry.UserID = v.(*admin.Actor).User.ID
		entry.Username = v.(*admin.Actor).User.Username
	}
	if err := db.WithContext(c.Request.Context()).Create(&entry).Error; err != nil {
		logger.Error("写入审计日志失败", map[string]any{"error": err})
	}
}

// Audit 记录写操作的参数与JSON响应；登录、密码、配置和上传不记录正文。
func Audit(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set("dawn.started", time.Now())
		write := c.Request.Method != http.MethodGet && c.Request.Method != http.MethodOptions
		private := strings.Contains(c.Request.URL.Path, "/auth/") || strings.Contains(c.Request.URL.Path, "/system/users") || strings.HasSuffix(c.Request.URL.Path, "/system/settings") || strings.Contains(c.Request.URL.Path, "/attachments/") || strings.HasSuffix(c.Request.URL.Path, "/cache/value")
		var writer *auditWriter
		if write && !private {
			query, _ := json.Marshal(c.Request.URL.Query())
			request := map[string]json.RawMessage{"query": json.RawMessage(auditJSON(query))}
			if strings.HasPrefix(c.ContentType(), "application/json") && c.Request.Body != nil {
				data, err := io.ReadAll(io.LimitReader(c.Request.Body, auditLimit+1))
				c.Request.Body = io.NopCloser(io.MultiReader(bytes.NewReader(data), c.Request.Body))
				if err == nil {
					if cleaned := auditJSON(data); cleaned != "" {
						request["body"] = json.RawMessage(cleaned)
					}
				}
			}
			raw, _ := json.Marshal(request)
			c.Set("admin.audit.request", string(raw))
			writer = &auditWriter{ResponseWriter: c.Writer}
			c.Writer = writer
		}
		c.Next()
		if writer != nil && !writer.truncated && strings.HasPrefix(c.Writer.Header().Get("Content-Type"), "application/json") {
			c.Set("admin.audit.response", auditJSON(writer.body.Bytes()))
		}
		if write {
			RecordAudit(db, c, "operation", c.Writer.Status(), "请求处理完成")
		}
	}
}
