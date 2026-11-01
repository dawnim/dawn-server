package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func Actor(c *gin.Context) *admin.Actor { return c.MustGet("dawn.actor").(*admin.Actor) }
func Require(a *admin.App, permission string) gin.HandlerFunc {
	return func(c *gin.Context) {
		token := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		if token == "" && c.Request.Method == http.MethodGet {
			token, _ = c.Cookie("dawn_session")
		}
		current, err := a.Authenticate(token)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			response.Fail(c, 401, "登录已失效")
			return
		}
		if err != nil {
			response.Report(c, err)
			return
		}
		if permission != "" && !current.Can(permission) {
			response.Fail(c, 403, "没有操作权限")
			return
		}
		c.Set("dawn.actor", current)
		c.Next()
	}
}
