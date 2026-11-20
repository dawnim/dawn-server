package clientip

import (
	"github.com/gin-gonic/gin"
	"github.com/prismgo/framework/container"
)

const resolvedHeader = "X-Dawn-Resolved-Client-IP"

// Resolve returns the application-managed resolver.
func Resolve() *Resolver {
	resolver, err := container.Make[*Resolver]("dawn.clientip")
	if err != nil {
		panic(err)
	}
	return resolver
}

// Register installs the resolver before the framework's HTTP middleware.
func Register(engine *gin.Engine) {
	resolver := Resolve()
	engine.TrustedPlatform = resolvedHeader
	engine.Use(func(c *gin.Context) {
		c.Request.Header.Del(resolvedHeader)
		c.Request.Header.Set(resolvedHeader, resolver.IP(c.Request))
		c.Next()
	})
}
