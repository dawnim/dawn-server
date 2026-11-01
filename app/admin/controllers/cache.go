package controllers

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dawnim/dawn-server/support/i18n"

	"github.com/dawnim/dawn-server/app/admin/middleware"
	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

func (a *Controller) CacheRoutes(g *gin.RouterGroup) {
	g.GET("/cache/definitions", middleware.Require(a.App, "dawn.cache.list"), func(c *gin.Context) {
		rows := make([]any, 0, len(a.CacheKeys))
		names := make([]string, 0, len(a.CacheKeys))
		for name := range a.CacheKeys {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			definition := a.CacheKeys[name]
			definition.Description = i18n.Text(c.GetString("admin.locale"), definition.Description)
			rows = append(rows, definition)
		}
		response.Respond(c, rows)
	})
	g.GET("/cache/instances", middleware.Require(a.App, "dawn.cache.list"), func(c *gin.Context) {
		d, ok := a.CacheKeys[c.Query("definition")]
		if !ok {
			response.Fail(c, 422, "缓存定义未注册")
			return
		}
		cursor, err := strconv.ParseUint(c.DefaultQuery("cursor", "0"), 10, 64)
		if err != nil {
			response.Fail(c, 422, "游标错误")
			return
		}
		pattern := a.Options.Name + ":" + a.Options.Environment + ":" + d.MatchPattern()
		keys, next, err := a.Redis.Scan(c.Request.Context(), cursor, pattern, 100).Result()
		if err != nil {
			response.Report(c, err)
			return
		}
		rows := []gin.H{}
		for _, key := range keys {
			if !d.Matches(a.Options.Name+":"+a.Options.Environment+":", key) {
				continue
			}
			ttl, err := a.Redis.TTL(c.Request.Context(), key).Result()
			if err != nil {
				response.Report(c, err)
				return
			}
			kind, err := a.Redis.Type(c.Request.Context(), key).Result()
			if err != nil {
				response.Report(c, err)
				return
			}
			seconds := int64(ttl)
			if ttl >= 0 {
				seconds = int64(ttl / time.Second)
			}
			rows = append(rows, gin.H{"key": key, "ttl": seconds, "type": kind})
		}
		response.Respond(c, gin.H{"data": rows, "cursor": next})
	})
	for _, method := range []string{"GET", "PUT", "DELETE"} {
		permission := "dawn.cache.list"
		if method == "PUT" {
			permission = "dawn.cache.update"
		}
		if method == "DELETE" {
			permission = "dawn.cache.delete"
		}
		g.Handle(method, "/cache/value", middleware.Require(a.App, permission), func(c *gin.Context) {
			d, ok := a.CacheKeys[c.Query("definition")]
			if !ok {
				response.Fail(c, 422, "缓存定义未注册")
				return
			}
			prefix := a.Options.Name + ":" + a.Options.Environment + ":"
			matched := d.Matches(prefix, c.Query("key"))
			var err error
			if !matched {
				response.Fail(c, 422, "Key不属于该定义")
				return
			}
			if method == "DELETE" {
				if err := a.Redis.Unlink(c.Request.Context(), c.Query("key")).Err(); err != nil {
					response.Report(c, err)
					return
				}
				response.Respond(c, nil)
				return
			}
			if method == "PUT" {
				if !d.Editable || !d.Readable {
					response.Fail(c, 403, "该缓存不允许编辑")
					return
				}
				var input struct {
					Field string `json:"field"`
					Value string `json:"value" binding:"max=65536"`
				}
				if c.ShouldBindJSON(&input) != nil {
					response.Fail(c, 422, "参数格式错误")
					return
				}
				ctx, key := c.Request.Context(), c.Query("key")
				kind, e := a.Redis.Type(ctx, key).Result()
				if e != nil {
					response.Report(c, e)
					return
				}
				if kind != d.Type {
					response.Fail(c, 422, "参数格式错误")
					return
				}
				switch d.Type {
				case "string":
					size, e := a.Redis.StrLen(ctx, key).Result()
					if e != nil {
						response.Report(c, e)
						return
					}
					if size > 65536 {
						response.Fail(c, 413, "文件超过大小限制")
						return
					}
					err = a.Redis.SetArgs(ctx, key, input.Value, redis.SetArgs{Mode: "XX", KeepTTL: true}).Err()
				case "hash":
					if input.Field == "" {
						response.Fail(c, 422, "参数格式错误")
						return
					}
					var changed int64
					changed, err = a.Redis.Eval(ctx, `if redis.call('exists',KEYS[1])==0 then return 0 end;redis.call('hset',KEYS[1],ARGV[1],ARGV[2]);return 1`, []string{key}, input.Field, input.Value).Int64()
					if err == nil && changed == 0 {
						response.Fail(c, 409, "参数格式错误")
						return
					}
				default:
					response.Fail(c, 422, "参数格式错误")
					return
				}
				if err != nil {
					response.Report(c, err)
					return
				}
				response.Respond(c, nil)
				return
			}
			if !d.Readable {
				response.Fail(c, 403, "此类Key不允许查看内容")
				return
			}
			var value any
			ctx, key := c.Request.Context(), c.Query("key")
			cursor, parseErr := strconv.ParseUint(c.DefaultQuery("cursor", "0"), 10, 64)
			if parseErr != nil {
				response.Fail(c, 422, "游标错误")
				return
			}
			switch d.Type {
			case "string":
				value, err = a.Redis.GetRange(ctx, key, 0, 65535).Result()
			case "hash":
				var data []string
				var next uint64
				data, next, err = a.Redis.HScan(ctx, key, cursor, "*", 100).Result()
				if d.Name == "ForeverSettings" && err == nil {
					group := strings.TrimPrefix(key, a.Options.Name+":"+a.Options.Environment+":"+d.Format(""))
					for i := 0; i+1 < len(data); i += 2 {
						if a.SettingDefinitions[group+"."+data[i]].Secret {
							data[i+1] = `"********"`
						}
					}
				}
				value = gin.H{"data": data, "cursor": next}
			case "set":
				var data []string
				var next uint64
				data, next, err = a.Redis.SScan(ctx, key, cursor, "*", 100).Result()
				value = gin.H{"data": data, "cursor": next}
			case "list":
				value, err = a.Redis.LRange(ctx, key, 0, 99).Result()
			case "zset":
				value, err = a.Redis.ZRangeWithScores(ctx, key, 0, 99).Result()
			default:
				response.Fail(c, 422, "不支持查看%s内容", d.Type)
				return
			}
			if err != nil {
				response.Report(c, err)
				return
			}
			response.Respond(c, value)
		})
	}
	g.DELETE("/cache/clear", middleware.Require(a.App, "dawn.cache.delete"), func(c *gin.Context) {
		ctx, prefix := c.Request.Context(), a.Options.Name+":"+a.Options.Environment+":"
		var cleared int64
		for _, d := range a.CacheKeys {
			if !d.Clearable {
				continue
			}
			var cursor uint64
			for {
				keys, next, err := a.Redis.Scan(ctx, cursor, prefix+d.MatchPattern(), 100).Result()
				if err != nil {
					response.Report(c, err)
					return
				}
				keys = slices.DeleteFunc(keys, func(key string) bool { return !d.Matches(prefix, key) })
				if len(keys) > 0 {
					count, err := a.Redis.Unlink(ctx, keys...).Result()
					if err != nil {
						response.Report(c, err)
						return
					}
					cleared += count
				}
				cursor = next
				if cursor == 0 {
					break
				}
			}
		}
		response.Respond(c, gin.H{"cleared": cleared})
	})

}
