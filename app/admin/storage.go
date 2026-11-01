package admin

import (
	"cmp"
	"context"
	"encoding/json"
	"github.com/prismgo/framework/config"
	"path/filepath"
	"strings"

	"github.com/dawnim/dawn-server/support/storage"
	fs "github.com/prismgo/framework/contracts/filesystem"
)

// FileDisk使用文件原始存储位置；切换上传驱动不改变已上传附件的位置。
func (a *App) FileDisk(ctx context.Context, location *storage.Config) (fs.Filesystem, storage.Config, error) {
	values := map[string]json.RawMessage{}
	cfg := storage.Config{}
	if location != nil {
		cfg = *location
	} else {
		raw, err := a.Setting(ctx, "upload.driver")
		if err != nil {
			return nil, cfg, err
		}
		if err = json.Unmarshal(raw, &cfg.Driver); err != nil {
			return nil, cfg, err
		}
	}
	for key := range a.SettingDefinitions {
		if strings.HasPrefix(key, "upload."+cfg.Driver+".") {
			raw, err := a.Setting(ctx, key)
			if err != nil {
				return nil, cfg, err
			}
			values[strings.TrimPrefix(key, "upload."+cfg.Driver+".")] = raw
		}
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return nil, cfg, err
	}
	var current struct {
		storage.Config
		AccessKeyValue string `json:"access_key"`
		SecretKeyValue string `json:"secret_key"`
	}
	if err := json.Unmarshal(raw, &current); err != nil {
		return nil, cfg, err
	}
	if location == nil {
		current.Driver = cfg.Driver
		cfg = current.Config
		if cfg.Driver == "local" {
			cfg.Root, err = filepath.Abs(cmp.Or(cfg.Root, config.GetString("filesystem.disks.local.root", "storage/app/private")))
			if err != nil {
				return nil, cfg, err
			}
		}
		cfg.Region = cmp.Or(cfg.Region, map[string]string{"s3": "us-east-1", "cos": "ap-guangzhou", "qiniu": "cn-east-1"}[cfg.Driver])
	}
	cfg.AccessKey, cfg.SecretKey = current.AccessKeyValue, current.SecretKeyValue
	disk, err := storage.Open(cfg)
	return disk, cfg, err
}
