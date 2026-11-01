package admin

import (
	"github.com/prismgo/framework/container"
	cc "github.com/prismgo/framework/contracts/container"
)

// From resolves the module from a specific application container.
func From(r cc.Resolver) (*App, error) {
	raw, err := r.Make("dawn.admin")
	if err != nil {
		return nil, err
	}
	return raw.(*App), nil
}

// Resolve 返回当前应用容器中的后台上下文。
func Resolve() *App {
	a, err := container.Make[*App]("dawn.admin")
	if err != nil {
		panic(err)
	}
	return a
}
