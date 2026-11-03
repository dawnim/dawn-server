package admin

import (
	"cmp"
	"context"
	"encoding/json"
	"github.com/prismgo/framework/config"
	"github.com/prismgo/framework/support"
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
		raw, err := a.Setting(ctx, "upload.visibility")
		if err != nil {
			return nil, cfg, err
		}
		if err := json.Unmarshal(raw, &current.Visibility); err != nil {
			return nil, cfg, err
		}
		current.Driver = cfg.Driver
		cfg = current.Config
		if cfg.Driver == "local" {
			disk := "public"
			if cfg.Visibility == "private" {
				disk = "local"
			}
			cfg.Root = cmp.Or(cfg.Root, config.GetString("filesystem.disks."+disk+".root", "storage/app/"+cfg.Visibility))
			if !filepath.IsAbs(cfg.Root) {
				cfg.Root = filepath.Join(support.BasePath(), cfg.Root)
			}
			if cfg.Visibility == "public" {
				cfg.URL = cmp.Or(cfg.URL, config.GetString("filesystem.disks.public.url", "/storage"))
			}
			cfg.Root, err = filepath.Abs(cfg.Root)
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
