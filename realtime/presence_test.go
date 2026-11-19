package realtime

import (
	"context"
	"github.com/alicebob/miniredis/v2"
	"github.com/dawnim/dawn-server/constants"
	"github.com/redis/go-redis/v9"
	"testing"
	"time"
)

func TestPresenceExpiresAndSeparatesApps(t *testing.T) {
	store := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: store.Addr()})
	defer client.Close()
	h := New(client, "presence-test:", "/")
	defer h.Close()
	ctx := context.Background()
	p := Principal{Realm: "support:1", Subject: "same-user"}
	key := h.key(constants.TempWSPresence, hash(p.Realm), hash(p.Subject))
	now := time.Now().UnixMilli()
	if err := client.ZAdd(ctx, key, redis.Z{Score: float64(now - 2*time.Minute.Milliseconds()), Member: "dead-worker"}, redis.Z{Score: float64(now), Member: "healthy-tab"}).Err(); err != nil {
		t.Fatal(err)
	}
	seen, err := h.Presence(ctx, p)
	if err != nil || seen != now {
		t.Fatal(seen, err)
	}
	if count := client.ZCard(ctx, key).Val(); count != 1 {
		t.Fatal("expired connection retained", count)
	}
	if other, err := h.Presence(ctx, Principal{Realm: "support:2", Subject: p.Subject}); err != nil || other != 0 {
		t.Fatal("cross-app presence", other, err)
	}
	if err = client.Expire(ctx, key, constants.TempWSPresence.TTL).Err(); err != nil {
		t.Fatal(err)
	}
	store.FastForward(constants.TempWSPresence.TTL)
	if seen, err = h.Presence(ctx, p); err != nil || seen != 0 {
		t.Fatal("worker crash retained online", seen, err)
	}
}
