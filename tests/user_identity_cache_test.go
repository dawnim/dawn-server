package tests

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/constants"
	"github.com/dawnim/dawn-server/support/identity"
	"gorm.io/gorm"
)

func TestUserIdentityCache(t *testing.T) {
	a := customerRuntime(t)
	ctx, key := context.Background(), a.Key(constants.ForeverSettings, identity.SettingKey)
	var reads atomic.Int32
	if err := a.DB.Callback().Query().Before("gorm:query").Register("test:identity_reads", func(tx *gorm.DB) {
		if tx.Statement.Table == "admin_settings" {
			reads.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.Redis.Del(ctx, key).Err(); err != nil {
		t.Fatal(err)
	}
	codec, err := identity.Load(ctx, a.DB)
	if err != nil || reads.Load() != 1 {
		t.Fatalf("cold read: %d, %v", reads.Load(), err)
	}
	if ttl, err := a.Redis.TTL(ctx, key).Result(); err != nil || ttl != -1 {
		t.Fatalf("identity cache must persist in Redis: %v, %v", ttl, err)
	}
	if value, err := a.Settings.Read(ctx, a.DB, identity.SettingKey, "key"); err != nil || string(value) != `"`+codec.Key+`"` || reads.Load() != 1 {
		t.Fatalf("identity did not reuse the settings cache: %d, %v", reads.Load(), err)
	}
	if revision, err := a.Redis.HGet(ctx, key, "__revision").Int(); err != nil || revision != 1 {
		t.Fatalf("shared settings revision missing: %d, %v", revision, err)
	}
	for range 3 {
		user := models.User{Nickname: "Cached identity"}
		if err := a.DB.Create(&user).Error; err != nil {
			t.Fatal(err)
		}
		if decoded, err := codec.Big2ID(user.UniqueID); err != nil || decoded != int64(user.ID) {
			t.Fatalf("cached user round trip: %d, %v", decoded, err)
		}
	}
	if reads.Load() != 1 {
		t.Fatalf("cache hits queried SQL: %d", reads.Load())
	}
	value, _ := codec.ID2Big(123)
	if err := a.Redis.Del(ctx, key).Err(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := identity.Load(ctx, a.DB)
	if err != nil || *reloaded != *codec || reads.Load() != 2 {
		t.Fatalf("cache miss changed configuration: %d, %v", reads.Load(), err)
	}
	if decoded, err := reloaded.Big2ID(value); err != nil || decoded != 123 {
		t.Fatalf("cache eviction broke decoding: %d, %v", decoded, err)
	}
	if constants.ForeverSettings.Matches(a.Options.Name+":"+a.Options.Environment+":", key) {
		t.Fatal("internal settings exposed to cache management")
	}
	if err := databaseCommand(a, "db:seed"); err != nil {
		t.Fatal(err)
	}
	if exists, err := a.Redis.Exists(ctx, key).Result(); err != nil || exists != 0 {
		t.Fatalf("seeding retained stale identity cache: %v, %v", exists, err)
	}
}
