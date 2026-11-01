package middleware

import (
	"slices"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

// CORS 支持任意来源或明确域名列表，跨域登录使用 Bearer 凭证。
func CORS(origins []string) gin.HandlerFunc {
	if len(origins) == 0 {
		return func(c *gin.Context) { c.Next() }
	}
	config := cors.DefaultConfig()
	config.AllowOrigins = origins
	config.AllowHeaders = []string{"Origin", "Authorization", "Content-Type"}
	config.ExposeHeaders = []string{"Content-Disposition"}
	config.AllowCredentials = !slices.Contains(origins, "*")
	return cors.New(config)
}
