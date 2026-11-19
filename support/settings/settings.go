// Package settings 为公开与内部配置共用分组Hash缓存。
package settings

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/dawnim/dawn-server/constants"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// Store 复用应用的Redis连接与命名空间，不另存配置副本。
type Store struct {
	Redis  redis.UniversalClient
	Prefix string
}

// Read 返回组内JSON字段；缺失字段返回nil，缺失记录保留数据库错误。
func (s *Store) Read(ctx context.Context, db *gorm.DB, group, field string) (json.RawMessage, error) {
	raw, err := s.Redis.HGet(ctx, s.Prefix+constants.ForeverSettings.Format(group), field).Result()
	if !errors.Is(err, redis.Nil) {
		return json.RawMessage(raw), err
	}
	var row struct {
		Value    string
		Revision int64
	}
	if err := db.Session(&gorm.Session{NewDB: true, Context: ctx}).Table("admin_settings").Where(map[string]any{"key": group}).Take(&row).Error; err != nil {
		return nil, err
	}
	values := map[string]json.RawMessage{}
	if err := json.Unmarshal([]byte(row.Value), &values); err != nil {
		return nil, err
	}
	return values[field], s.Write(ctx, group, values, row.Revision)
}

// Write 只接受较新的配置版本，避免多实例并发时旧值回写。
func (s *Store) Write(ctx context.Context, group string, values map[string]json.RawMessage, revision int64) error {
	args := []any{revision}
	for field, value := range values {
		args = append(args, field, string(value))
	}
	return s.Redis.Eval(ctx, `local old=tonumber(redis.call('hget',KEYS[1],'__revision') or '-1'); if old>tonumber(ARGV[1]) then return 0 end; redis.call('del',KEYS[1]); redis.call('hset',KEYS[1],'__revision',ARGV[1]); for i=2,#ARGV,2 do redis.call('hset',KEYS[1],ARGV[i],ARGV[i+1]); end; return 1`, []string{s.Prefix + constants.ForeverSettings.Format(group)}, args...).Err()
}
