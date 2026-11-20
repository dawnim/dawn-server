package controllers

import (
	"cmp"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"time"

	"github.com/dawnim/dawn-server/app/admin/middleware"
	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/support/supervisor"
	"github.com/gin-gonic/gin"
	"github.com/prismgo/framework/config"
	"github.com/prismgo/framework/route"
	"github.com/prismgo/framework/support"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"
)

func (a *Controller) ServerRoutes(g *route.Registrar) {
	g.Get("/server", middleware.Require(a.App, "dawn.server.list"), func(c *gin.Context) {
		var memory runtime.MemStats
		runtime.ReadMemStats(&memory)
		sqlDB, err := a.DB.DB()
		if err != nil {
			response.Report(c, err)
			return
		}
		ctx := c.Request.Context()
		system := gin.H{}
		unavailable := gin.H{}
		collect := func(key string, value any, err error) {
			if err != nil {
				unavailable[key] = err.Error()
			} else {
				system[key] = value
			}
		}
		value, e := host.InfoWithContext(ctx)
		collect("host", value, e)
		memoryInfo, e := mem.VirtualMemoryWithContext(ctx)
		collect("memory", memoryInfo, e)
		diskInfo, e := disk.UsageWithContext(ctx, cmp.Or(config.GetString("dawn.monitor.disk_path"), support.BasePath()))
		collect("disk", diskInfo, e)
		cpuUsage, e := cpu.PercentWithContext(ctx, 100*time.Millisecond, false)
		collect("cpu_percent", cpuUsage, e)
		cpuInfo, e := cpu.InfoWithContext(ctx)
		collect("cpu", cpuInfo, e)
		loadInfo, e := load.AvgWithContext(ctx)
		collect("load", loadInfo, e)
		pids, e := process.PidsWithContext(ctx)
		collect("process_count", len(pids), e)
		current, err := process.NewProcessWithContext(ctx, int32(os.Getpid()))
		if err != nil {
			response.Report(c, err)
			return
		}
		processMemory, e := current.MemoryInfoWithContext(ctx)
		collect("process_memory", processMemory, e)
		threads, e := current.NumThreadsWithContext(ctx)
		collect("threads", threads, e)
		started, e := current.CreateTimeWithContext(ctx)
		collect("uptime_seconds", (time.Now().UnixMilli()-started)/1000, e)
		processCPU, e := current.PercentWithContext(ctx, 100*time.Millisecond)
		collect("process_cpu_percent", processCPU, e)
		embedding := gin.H{"enabled": config.GetBool("dawn.embedding.enabled", false)}
		if embedding["enabled"] == true {
			socket := support.BasePath(config.GetString("dawn.runtime_directory", "storage/framework"), "dawn.sock")
			statusCtx, cancelStatus := context.WithTimeout(ctx, time.Second)
			defer cancelStatus()
			status, _, statusErr := supervisor.Control(statusCtx, socket, "embedding", "status")
			if statusErr != nil {
				embedding["error"] = statusErr.Error()
			} else if len(status.Services) > 0 {
				embedding["service"] = status.Services[0]
			}
			host := config.GetString("dawn.embedding.host", "127.0.0.1")
			if host == "0.0.0.0" || host == "::" {
				host = "127.0.0.1"
			}
			probeCtx, cancel := context.WithTimeout(ctx, time.Second)
			defer cancel()
			req, _ := http.NewRequestWithContext(probeCtx, http.MethodGet, "http://"+net.JoinHostPort(host, strconv.Itoa(config.GetInt("dawn.embedding.port", 8852)))+"/health", nil)
			res, probeErr := http.DefaultClient.Do(req)
			if probeErr == nil {
				defer res.Body.Close()
				var health map[string]any
				if res.StatusCode == http.StatusOK && json.NewDecoder(res.Body).Decode(&health) == nil {
					embedding["health"] = health
				}
			}
		}
		var indexes []struct {
			models.KnowledgeIndex
			AppName string `json:"app_name"`
		}
		if indexErr := a.DB.WithContext(ctx).Table("knowledge_indexes").Select("knowledge_indexes.*, support_apps.name AS app_name").Joins("JOIN support_apps ON support_apps.id = knowledge_indexes.app_id AND support_apps.deleted_at = 0").Where("knowledge_indexes.status IN ?", []string{"active", "building", "failed", "cleanup"}).Order("knowledge_indexes.id DESC").Scan(&indexes).Error; indexErr != nil {
			embedding["indexes_error"] = indexErr.Error()
		} else {
			embedding["indexes"] = indexes
		}
		response.Respond(c, gin.H{"pid": os.Getpid(), "system": system, "unavailable": unavailable, "name": a.Options.Name, "go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH, "cpus": runtime.NumCPU(), "goroutines": runtime.NumGoroutine(), "heap_bytes": memory.HeapAlloc, "heap_sys": memory.HeapSys, "system_bytes": memory.Sys, "db": sqlDB.Stats(), "driver": a.DB.Dialector.Name(), "embedding": embedding})
	})

}
