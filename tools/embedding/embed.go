// Package embedding 随扩展分发向量服务脚本，运行环境和权重保存在使用方项目。
package embedding

import (
	"embed"
	"os"
	"path/filepath"
)

//go:embed main.py requirements.txt
var files embed.FS

// Publish 更新项目中的服务脚本，不修改Python环境或模型缓存。
func Publish(directory string) error {
	if err := os.MkdirAll(directory, 0755); err != nil {
		return err
	}
	for _, name := range []string{"main.py", "requirements.txt"} {
		content, err := files.ReadFile(name)
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(directory, name), content, 0644); err != nil {
			return err
		}
	}
	return nil
}
