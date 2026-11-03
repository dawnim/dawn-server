package admin

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"

	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/constants"
	"github.com/redis/go-redis/v9"
)

// 首次读取原子创建随机版本，版本Key被手动清理后也不会复用旧版本。
func (a *App) userCacheVersion(ctx context.Context, id uint) (string, error) {
	return a.Redis.Eval(ctx, `local v=redis.call('get',KEYS[1]); if v then return v end; redis.call('del',KEYS[2],KEYS[3],KEYS[4]); redis.call('set',KEYS[1],ARGV[1]); return ARGV[1]`, []string{a.Key(constants.ForeverUserCacheVersion, id), a.Key(constants.TempUserInfo, id), a.Key(constants.TempUserPermissions, id), a.Key(constants.TempUserMenus, id)}, rand.Text()).Text()
}

func cachedUserData[T any](ctx context.Context, a *App, definition constants.Definition, id uint, version string, load func() (T, error)) (T, error) {
	var value T
	key := a.Key(definition, id)
	raw, err := a.Redis.Get(ctx, key).Bytes()
	if err == nil {
		err = json.Unmarshal(raw, &value)
		return value, err
	}
	if !errors.Is(err, redis.Nil) {
		return value, err
	}
	value, err = load()
	if err != nil {
		return value, err
	}
	raw, err = json.Marshal(value)
	if err != nil {
		return value, err
	}
	// 版本校验与写入原子执行，禁止清理前的请求把旧授权写回。
	return value, a.Redis.Eval(ctx, `if (redis.call('get',KEYS[2]) or '')~=ARGV[1] then return 0 end; redis.call('set',KEYS[1],ARGV[2],'PX',ARGV[3]); return 1`, []string{key, a.Key(constants.ForeverUserCacheVersion, id)}, version, raw, definition.TTL.Milliseconds()).Err()
}

// ClearUserCaches 清理指定用户的资料、授权和菜单；不传ID时清理所有用户。
// 自定义代码直接修改账号、角色或菜单后，在事务提交后调用。
func (a *App) ClearUserCaches(ctx context.Context, ids ...uint) error {
	if len(ids) == 0 {
		if err := a.DB.WithContext(ctx).Unscoped().Model(&models.User{}).Pluck("id", &ids).Error; err != nil {
			return err
		}
	}
	pipe := a.Redis.TxPipeline()
	keys := make([]string, 0, len(ids)*3)
	for _, id := range ids {
		pipe.Set(ctx, a.Key(constants.ForeverUserCacheVersion, id), rand.Text(), 0)
		for _, definition := range []constants.Definition{constants.TempUserInfo, constants.TempUserPermissions, constants.TempUserMenus} {
			keys = append(keys, a.Key(definition, id))
		}
	}
	if len(keys) == 0 {
		return nil
	}
	pipe.Del(ctx, keys...)
	_, err := pipe.Exec(ctx)
	return err
}
