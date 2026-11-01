package admin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/dawnim/dawn-server/support/i18n"

	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/constants"
	"github.com/prismgo/framework/encryption"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type SettingOption struct {
	Label string `json:"label"`
	Value any    `json:"value"`
}
type SettingDefinition struct {
	Key     string          `json:"key"`
	Title   string          `json:"title"`
	Group   string          `json:"group"`
	Kind    string          `json:"kind"`
	Input   string          `json:"input"`
	Options []SettingOption `json:"options,omitempty"`
	Remark  string          `json:"remark,omitempty"`
	Sort    int             `json:"sort"`
	Default any             `json:"default"`
	Secret  bool            `json:"secret"`
	When    map[string]any  `json:"when,omitempty"`
}

func systemSettings() []SettingDefinition {
	return []SettingDefinition{
		{Key: "site.title", Title: "站点名称", Group: "站点配置", Kind: "string", Input: "input", Default: "Dawn", Sort: 0},
		{Key: "site.logo", Title: "站点Logo", Group: "站点配置", Kind: "string", Input: "image", Default: "", Sort: 1},
		{Key: "site.copyright", Title: "版权信息", Group: "站点配置", Kind: "string", Input: "textarea", Default: "", Sort: 2},
		{Key: "site.description", Title: "网站描述", Group: "站点配置", Kind: "string", Input: "textarea", Default: "", Sort: 3},
		{Key: "site.keywords", Title: "网站关键字", Group: "站点配置", Kind: "string", Input: "input", Default: "", Sort: 4},
		{Key: "site.record_number", Title: "备案号", Group: "站点配置", Kind: "string", Input: "input", Default: "", Sort: 5},
		{Key: "upload.driver", Title: "上传模式", Group: "上传配置", Kind: "string", Input: "select", Default: "local", Sort: 6, Options: []SettingOption{{Label: "本地上传", Value: "local"}, {Label: "阿里云OSS", Value: "oss"}, {Label: "七牛云", Value: "qiniu"}, {Label: "腾讯云COS", Value: "cos"}, {Label: "亚马逊S3", Value: "s3"}}},
		{Key: "upload.max_size", Title: "上传大小", Group: "上传配置", Kind: "number", Input: "number", Default: 104857600, Sort: 7, Remark: "单位Byte，1MB=1024×1024Byte"},
		{Key: "upload.extensions", Title: "文件类型", Group: "上传配置", Kind: "string", Input: "input", Default: "txt,doc,docx,xls,xlsx,ppt,pptx,rar,zip,7z,gz,pdf,wps,md,jpg,png,jpeg,gif,svg,bmp,webp,mp4,pem,crt", Sort: 8, Remark: "扩展名用逗号分隔"},
		{Key: "upload.image_extensions", Title: "图片类型", Group: "上传配置", Kind: "string", Input: "input", Default: "jpg,png,jpeg,gif,svg,bmp,webp", Sort: 9},
		{Key: "upload.local.root", Title: "本地存储路径", Group: "上传配置", Kind: "string", Input: "input", Default: "", Sort: 10, Remark: "留空使用filesystem配置中的默认磁盘", When: map[string]any{"upload.driver": "local"}},
		{Key: "upload.oss.access_key", Title: "Access Key", Group: "上传配置", Kind: "string", Input: "password", Default: "", Sort: 11, Secret: true, When: map[string]any{"upload.driver": "oss"}},
		{Key: "upload.oss.secret_key", Title: "Secret Key", Group: "上传配置", Kind: "string", Input: "password", Default: "", Sort: 12, Secret: true, When: map[string]any{"upload.driver": "oss"}},
		{Key: "upload.oss.bucket", Title: "存储桶", Group: "上传配置", Kind: "string", Input: "input", Default: "", Sort: 13, When: map[string]any{"upload.driver": "oss"}},
		{Key: "upload.oss.endpoint", Title: "Endpoint", Group: "上传配置", Kind: "string", Input: "input", Default: "", Sort: 14, Remark: "https://oss-cn-hangzhou.aliyuncs.com", When: map[string]any{"upload.driver": "oss"}},
		{Key: "upload.oss.region", Title: "区域", Group: "上传配置", Kind: "string", Input: "input", Default: "", Sort: 15, When: map[string]any{"upload.driver": "oss"}},
		{Key: "upload.oss.prefix", Title: "存储目录", Group: "上传配置", Kind: "string", Input: "input", Default: "", Sort: 16, When: map[string]any{"upload.driver": "oss"}},
		{Key: "upload.qiniu.access_key", Title: "Access Key", Group: "上传配置", Kind: "string", Input: "password", Default: "", Sort: 17, Secret: true, When: map[string]any{"upload.driver": "qiniu"}},
		{Key: "upload.qiniu.secret_key", Title: "Secret Key", Group: "上传配置", Kind: "string", Input: "password", Default: "", Sort: 18, Secret: true, When: map[string]any{"upload.driver": "qiniu"}},
		{Key: "upload.qiniu.bucket", Title: "存储桶", Group: "上传配置", Kind: "string", Input: "input", Default: "", Sort: 19, When: map[string]any{"upload.driver": "qiniu"}},
		{Key: "upload.qiniu.endpoint", Title: "Endpoint", Group: "上传配置", Kind: "string", Input: "input", Default: "", Sort: 20, Remark: "https://s3.cn-east-1.qiniucs.com", When: map[string]any{"upload.driver": "qiniu"}},
		{Key: "upload.qiniu.region", Title: "区域", Group: "上传配置", Kind: "string", Input: "input", Default: "", Sort: 21, When: map[string]any{"upload.driver": "qiniu"}},
		{Key: "upload.qiniu.prefix", Title: "存储目录", Group: "上传配置", Kind: "string", Input: "input", Default: "", Sort: 22, When: map[string]any{"upload.driver": "qiniu"}},
		{Key: "upload.cos.access_key", Title: "Access Key", Group: "上传配置", Kind: "string", Input: "password", Default: "", Sort: 23, Secret: true, When: map[string]any{"upload.driver": "cos"}},
		{Key: "upload.cos.secret_key", Title: "Secret Key", Group: "上传配置", Kind: "string", Input: "password", Default: "", Sort: 24, Secret: true, When: map[string]any{"upload.driver": "cos"}},
		{Key: "upload.cos.bucket", Title: "存储桶", Group: "上传配置", Kind: "string", Input: "input", Default: "", Sort: 25, When: map[string]any{"upload.driver": "cos"}},
		{Key: "upload.cos.endpoint", Title: "Endpoint", Group: "上传配置", Kind: "string", Input: "input", Default: "", Sort: 26, Remark: "https://cos.ap-guangzhou.myqcloud.com", When: map[string]any{"upload.driver": "cos"}},
		{Key: "upload.cos.region", Title: "区域", Group: "上传配置", Kind: "string", Input: "input", Default: "", Sort: 27, When: map[string]any{"upload.driver": "cos"}},
		{Key: "upload.cos.prefix", Title: "存储目录", Group: "上传配置", Kind: "string", Input: "input", Default: "", Sort: 28, When: map[string]any{"upload.driver": "cos"}},
		{Key: "upload.s3.access_key", Title: "Access Key", Group: "上传配置", Kind: "string", Input: "password", Default: "", Sort: 29, Secret: true, When: map[string]any{"upload.driver": "s3"}},
		{Key: "upload.s3.secret_key", Title: "Secret Key", Group: "上传配置", Kind: "string", Input: "password", Default: "", Sort: 30, Secret: true, When: map[string]any{"upload.driver": "s3"}},
		{Key: "upload.s3.bucket", Title: "存储桶", Group: "上传配置", Kind: "string", Input: "input", Default: "", Sort: 31, When: map[string]any{"upload.driver": "s3"}},
		{Key: "upload.s3.endpoint", Title: "Endpoint", Group: "上传配置", Kind: "string", Input: "input", Default: "", Sort: 32, Remark: "https://s3.ap-southeast-1.amazonaws.com", When: map[string]any{"upload.driver": "s3"}},
		{Key: "upload.s3.region", Title: "区域", Group: "上传配置", Kind: "string", Input: "input", Default: "", Sort: 33, When: map[string]any{"upload.driver": "s3"}},
		{Key: "upload.s3.prefix", Title: "存储目录", Group: "上传配置", Kind: "string", Input: "input", Default: "", Sort: 34, When: map[string]any{"upload.driver": "s3"}},
		{Key: "upload.s3.path_style", Title: "Path Style", Group: "上传配置", Kind: "boolean", Input: "switch", Default: false, Sort: 35, When: map[string]any{"upload.driver": "s3"}},
		{Key: "upload.s3.acl", Title: "访问权限", Group: "上传配置", Kind: "string", Input: "select", Default: "", Sort: 36, Options: []SettingOption{{Label: "跟随存储桶配置", Value: ""}, {Label: "private", Value: "private"}, {Label: "public-read", Value: "public-read"}}, When: map[string]any{"upload.driver": "s3"}},
		{Key: "upload.local.url", Title: "本地访问域名", Group: "上传配置", Kind: "string", Input: "input", Default: "", Sort: 35, When: map[string]any{"upload.driver": "local"}},
		{Key: "upload.oss.url", Title: "访问域名/CDN", Group: "上传配置", Kind: "string", Input: "input", Default: "", Sort: 35, When: map[string]any{"upload.driver": "oss"}},
		{Key: "upload.qiniu.url", Title: "访问域名/CDN", Group: "上传配置", Kind: "string", Input: "input", Default: "", Sort: 35, When: map[string]any{"upload.driver": "qiniu"}},
		{Key: "upload.cos.url", Title: "访问域名/CDN", Group: "上传配置", Kind: "string", Input: "input", Default: "", Sort: 35, When: map[string]any{"upload.driver": "cos"}},
		{Key: "upload.s3.url", Title: "访问域名/CDN", Group: "上传配置", Kind: "string", Input: "input", Default: "", Sort: 35, When: map[string]any{"upload.driver": "s3"}},
		{Key: "mail.host", Title: "SMTP服务器", Group: "邮件服务", Kind: "string", Input: "input", Default: "", Sort: 37},
		{Key: "mail.port", Title: "SMTP端口", Group: "邮件服务", Kind: "number", Input: "number", Default: 587, Sort: 38},
		{Key: "mail.username", Title: "SMTP用户名", Group: "邮件服务", Kind: "string", Input: "input", Default: "", Sort: 39},
		{Key: "mail.password", Title: "SMTP密码", Group: "邮件服务", Kind: "string", Input: "password", Default: "", Sort: 40, Secret: true},
		{Key: "mail.security", Title: "加密方式", Group: "邮件服务", Kind: "string", Input: "select", Default: "tls", Sort: 41, Options: []SettingOption{{Label: "STARTTLS", Value: "tls"}, {Label: "SSL/TLS", Value: "ssl"}, {Label: "不加密（仅限本地测试）", Value: "none"}}},
		{Key: "mail.from", Title: "默认发件人", Group: "邮件服务", Kind: "string", Input: "input", Default: "", Sort: 42},
		{Key: "mail.from_name", Title: "发件名称", Group: "邮件服务", Kind: "string", Input: "input", Default: "Dawn", Sort: 43},
	}
}

// Setting 返回组内单个JSON值，保密字段只在服务端解密。
func (a *App) Setting(ctx context.Context, key string) (json.RawMessage, error) {
	definition, ok := a.SettingDefinitions[key]
	if !ok {
		return nil, i18n.Errorf("配置未注册: %s", key)
	}
	group, field, _ := strings.Cut(key, ".")
	raw, err := a.Redis.HGet(ctx, a.Key(constants.ForeverSettings, group), field).Result()
	if errors.Is(err, redis.Nil) {
		var row models.Setting
		err = a.DB.WithContext(ctx).Where(map[string]any{"key": group}).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return json.Marshal(definition.Default)
		}
		if err != nil {
			return nil, err
		}
		values := map[string]json.RawMessage{}
		if err = json.Unmarshal([]byte(row.Value), &values); err != nil {
			return nil, err
		}
		if err = a.CacheSetting(ctx, row); err != nil {
			return nil, err
		}
		value, ok := values[field]
		if !ok {
			return json.Marshal(definition.Default)
		}
		raw = string(value)
	}
	if err != nil {
		return nil, err
	}
	value := json.RawMessage(raw)
	if definition.Secret {
		var ciphertext string
		if err := json.Unmarshal(value, &ciphertext); err != nil {
			return nil, err
		}
		if ciphertext == "" {
			return json.Marshal(definition.Default)
		}
		plain, err := encryption.DecryptString(ctx, ciphertext)
		return json.RawMessage(plain), err
	}
	return value, nil
}

// CacheSetting 只接受较新的配置版本，避免多实例并发时旧值回写。
func (a *App) CacheSetting(ctx context.Context, row models.Setting) error {
	values := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(row.Value), &values); err != nil {
		return err
	}
	args := []any{row.Revision}
	for field, value := range values {
		args = append(args, field, string(value))
	}
	return a.Redis.Eval(ctx, `local old=tonumber(redis.call('hget',KEYS[1],'__revision') or '-1'); if old>tonumber(ARGV[1]) then return 0 end; redis.call('del',KEYS[1]); redis.call('hset',KEYS[1],'__revision',ARGV[1]); for i=2,#ARGV,2 do redis.call('hset',KEYS[1],ARGV[i],ARGV[i+1]); end; return 1`, []string{a.Key(constants.ForeverSettings, row.Key)}, args...).Err()
}

// SyncSettings 补齐组和新字段，不覆盖已有配置。
func (a *App) SyncSettings(ctx context.Context) error {
	groups := map[string]map[string]json.RawMessage{}
	names := map[string]string{}
	for _, d := range a.SettingDefinitions {
		group, field, _ := strings.Cut(d.Key, ".")
		if groups[group] == nil {
			groups[group] = map[string]json.RawMessage{}
		}
		value, err := json.Marshal(d.Default)
		if err != nil {
			return err
		}
		if d.Secret {
			value = json.RawMessage(`""`)
		}
		groups[group][field] = value
		names[group] = i18n.Text("zh", d.Group)
	}
	for key, defaults := range groups {
		var row models.Setting
		err := a.DB.WithContext(ctx).Transaction(func(db *gorm.DB) error {
			raw, err := json.Marshal(defaults)
			if err != nil {
				return err
			}
			row = models.Setting{Key: key, Name: names[key], Value: string(raw), Revision: 1}
			if err := db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "key"}}, DoNothing: true}).Create(&row).Error; err != nil {
				return err
			}
			if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).Where(map[string]any{"key": key}).First(&row).Error; err != nil {
				return err
			}
			values := map[string]json.RawMessage{}
			if err := json.Unmarshal([]byte(row.Value), &values); err != nil {
				return err
			}
			for field, value := range defaults {
				if _, ok := values[field]; !ok {
					values[field] = value
				}
			}
			raw, err = json.Marshal(values)
			if err != nil {
				return err
			}
			row.Name, row.Value, row.Revision = names[key], string(raw), row.Revision+1
			return db.Save(&row).Error
		})
		if err != nil {
			return err
		}
		if err := a.CacheSetting(ctx, row); err != nil {
			return err
		}
	}
	return nil
}
