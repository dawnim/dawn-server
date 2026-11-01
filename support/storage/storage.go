package storage

import (
	"encoding/json"
	"time"

	fs "github.com/prismgo/framework/contracts/filesystem"
	"github.com/prismgo/framework/filesystem"
	"github.com/prismgo/oss"
)

// Config中的位置随文件保存，密钥只在请求中从保密配置读取。
type Config struct {
	Driver    string `json:"driver"`
	Root      string `json:"root,omitempty"`
	Bucket    string `json:"bucket,omitempty"`
	Endpoint  string `json:"endpoint,omitempty"`
	Region    string `json:"region,omitempty"`
	Prefix    string `json:"prefix,omitempty"`
	URL       string `json:"url,omitempty"`
	PathStyle bool   `json:"path_style,omitempty"`
	ACL       string `json:"acl,omitempty"`
	AccessKey string `json:"-"`
	SecretKey string `json:"-"`
}

func Open(cfg Config) (fs.Repository, error) {
	disk := filesystem.DiskConfig{Driver: cfg.Driver, Root: cfg.Root, URL: cfg.URL, Visibility: "private", Options: map[string]any{"driver": cfg.Driver, "root": cfg.Root, "bucket": cfg.Bucket, "endpoint": cfg.Endpoint, "region": cfg.Region, "prefix": cfg.Prefix, "url": cfg.URL, "path_style": cfg.PathStyle, "acl": cfg.ACL, "access_key": cfg.AccessKey, "secret_key": cfg.SecretKey}, OSS: filesystem.OSSConfig{Bucket: cfg.Bucket, Endpoint: cfg.Endpoint, AccessKey: cfg.AccessKey, SecretKey: cfg.SecretKey, Prefix: cfg.Prefix, URL: cfg.URL, Visibility: "private", Timeout: time.Minute}}
	manager, err := filesystem.NewManager(filesystem.Config{Default: cfg.Driver, Disks: map[string]filesystem.DiskConfig{cfg.Driver: disk}})
	if err != nil {
		return nil, err
	}
	Register(manager)
	return manager.Disk(cfg.Driver), nil
}

// Register向当前应用的文件系统管理器添加云存储驱动。
func Register(manager *filesystem.Manager) {
	manager.Extend("oss", func(c filesystem.DriverFactoryContext) (filesystem.Driver, error) { return oss.NewDriver(c.Config.OSS) })
	for _, driver := range []string{"s3", "cos", "qiniu"} {
		manager.Extend(driver, func(c filesystem.DriverFactoryContext) (filesystem.Driver, error) {
			raw, err := json.Marshal(c.Config.Options)
			if err != nil {
				return nil, err
			}
			var cfg Config
			if err := json.Unmarshal(raw, &cfg); err != nil {
				return nil, err
			}
			cfg.AccessKey, _ = c.Config.Options["access_key"].(string)
			cfg.SecretKey, _ = c.Config.Options["secret_key"].(string)
			return NewS3(cfg), nil
		})
	}
}
