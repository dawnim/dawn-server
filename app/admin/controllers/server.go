package controllers

import (
	"cmp"
	"os"
	"runtime"
	"time"

	"github.com/dawnim/dawn-server/app/admin/middleware"
	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/gin-gonic/gin"
	"github.com/prismgo/framework/config"
	"github.com/prismgo/framework/support"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"
)

func (a *Controller) ServerRoutes(g *gin.RouterGroup) {
	g.GET("/system/server", middleware.Require(a.App, "dawn.server.list"), func(c *gin.Context) {
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
		response.Respond(c, gin.H{"pid": os.Getpid(), "system": system, "unavailable": unavailable, "name": a.Options.Name, "go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH, "cpus": runtime.NumCPU(), "goroutines": runtime.NumGoroutine(), "heap_bytes": memory.HeapAlloc, "heap_sys": memory.HeapSys, "system_bytes": memory.Sys, "db": sqlDB.Stats(), "driver": a.DB.Dialector.Name()})
	})

}
