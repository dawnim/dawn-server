package middleware

import (
	"github.com/dawnim/dawn-server/support/i18n"
	"github.com/gin-gonic/gin"
)

func Locale(c *gin.Context) {
	locale := i18n.Match(c.GetHeader("Accept-Language"))
	c.Set("admin.locale", locale)
	c.Request = c.Request.WithContext(i18n.WithLocale(c.Request.Context(), locale))
	c.Next()
}
