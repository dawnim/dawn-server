package response

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/dawnim/dawn-server/support/i18n"

	"github.com/gin-gonic/gin"
	"github.com/prismgo/framework/logger"
	"gorm.io/gorm"
)

func Respond(c *gin.Context, data any) {
	c.JSON(http.StatusOK, gin.H{"code": 200, "message": i18n.Text(c.GetString("admin.locale"), "操作成功"), "data": data})
}
func Fail(c *gin.Context, status int, message string, args ...any) {
	c.AbortWithStatusJSON(status, gin.H{"code": status, "message": i18n.Text(c.GetString("admin.locale"), message, args...), "data": nil})
}
func Report(c *gin.Context, err error, status ...int) {
	if err == nil {
		return
	}
	if len(status) > 0 {
		Fail(c, status[0], i18n.Message(err, c.GetString("admin.locale")))
		return
	}
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		Fail(c, 404, "记录不存在")
	case errors.Is(err, gorm.ErrDuplicatedKey):
		Fail(c, 409, "记录已存在")
	default:
		logger.Error("后台请求失败", map[string]any{"error": err, "path": c.Request.URL.Path})
		Fail(c, 500, "操作失败,请查看服务日志")
	}
}

// ID 统一解析路由主键, 避免非法值落入无条件查询.
func ID(c *gin.Context) uint {
	value, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || value == 0 {
		Fail(c, 422, "无效的记录ID")
		return 0
	}
	return uint(value)
}
