package supervisor

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

// RuntimeStats 是Go进程通过本地控制通道上报的运行时指标。
type RuntimeStats struct {
	PID        int `json:"pid"`
	Goroutines int `json:"goroutines"`
}

// ProcessStats 是操作系统采样的进程指标；不可用的指标省略。
type ProcessStats struct {
	Name       string         `json:"name"`
	PID        int            `json:"pid"`
	User       string         `json:"user"`
	RSS        *uint64        `json:"rss_bytes,omitempty"`
	CPU        *float64       `json:"cpu_percent,omitempty"`
	Threads    *int32         `json:"threads,omitempty"`
	Goroutines *int           `json:"goroutines,omitempty"`
	StartedAt  int64          `json:"started_at"`
	Children   []ProcessStats `json:"children,omitempty"`
}

type sample struct {
	at      time.Time
	cpu     float64
	started int64
}
type runtimeSample struct {
	RuntimeStats
	at time.Time
}
type monitor struct {
	mu        sync.Mutex
	runtimes  map[int]runtimeSample
	previous  map[int]sample
	processes map[int]ProcessStats
	at        time.Time
}

func processName(name string, args []string) string {
	if len(args) < 2 {
		return name
	}
	switch args[1] {
	case "horizon:supervisor":
		if len(args) > 2 {
			return args[2]
		}
	case "horizon:work":
		for _, arg := range args[2:] {
			if value, ok := strings.CutPrefix(arg, "--name="); ok {
				return value
			}
		}
	}
	return name
}

// ReportRuntime 向项目的本地控制通道上报当前Go进程的协程数，随context退出。
func ReportRuntime(ctx context.Context, socket string) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		conn, err := (&net.Dialer{Timeout: 200 * time.Millisecond}).DialContext(ctx, "unix", socket)
		if err == nil {
			_ = conn.SetDeadline(time.Now().Add(200 * time.Millisecond))
			_ = json.NewEncoder(conn).Encode(request{Action: "metrics", Runtime: &RuntimeStats{PID: os.Getpid(), Goroutines: runtime.NumGoroutine()}})
			_ = conn.Close()
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *monitor) record(value RuntimeStats) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runtimes == nil {
		m.runtimes = map[int]runtimeSample{}
	}
	m.runtimes[value.PID] = runtimeSample{RuntimeStats: value, at: time.Now()}
}

func (m *monitor) enrich(ctx context.Context, result *Response) {
	m.mu.Lock()
	defer m.mu.Unlock()
	refresh := time.Since(m.at) >= 2*time.Second
	for _, service := range result.Services {
		if service.PID > 0 {
			if _, ok := m.processes[service.PID]; !ok {
				refresh = true
			}
		}
	}
	if refresh {
		previous := m.previous
		m.previous, m.processes = map[int]sample{}, map[int]ProcessStats{}
		for pid, report := range m.runtimes {
			if time.Since(report.at) > 10*time.Second {
				delete(m.runtimes, pid)
			}
		}
		var inspect func(int, bool) ProcessStats
		inspect = func(pid int, children bool) ProcessStats {
			value := ProcessStats{PID: pid}
			p, err := process.NewProcessWithContext(ctx, int32(pid))
			if err != nil {
				return value
			}
			value.Name, _ = p.NameWithContext(ctx)
			if args, err := p.CmdlineSliceWithContext(ctx); err == nil {
				value.Name = processName(value.Name, args)
			}
			value.User, _ = p.UsernameWithContext(ctx)
			value.StartedAt, _ = p.CreateTimeWithContext(ctx)
			if memory, err := p.MemoryInfoWithContext(ctx); err == nil {
				value.RSS = &memory.RSS
			}
			if threads, err := p.NumThreadsWithContext(ctx); err == nil {
				value.Threads = &threads
			}
			if report, ok := m.runtimes[pid]; ok {
				n := report.Goroutines
				value.Goroutines = &n
			}
			if pid == os.Getpid() {
				n := runtime.NumGoroutine()
				value.Goroutines = &n
			}
			if usage, err := p.TimesWithContext(ctx); err == nil {
				now, total := time.Now(), usage.User+usage.System
				if old, ok := previous[pid]; ok && old.started == value.StartedAt && total >= old.cpu {
					percent := (total - old.cpu) / now.Sub(old.at).Seconds() * 100
					value.CPU = &percent
				}
				m.previous[pid] = sample{at: now, cpu: total, started: value.StartedAt}
			}
			if children {
				list, _ := p.ChildrenWithContext(ctx)
				for _, child := range list {
					value.Children = append(value.Children, inspect(int(child.Pid), true))
				}
			}
			m.processes[pid] = value
			return value
		}
		inspect(result.PID, false)
		for _, service := range result.Services {
			if service.PID > 0 {
				inspect(service.PID, true)
			}
		}
		m.at = time.Now()
	}
	result.Manager = m.processes[result.PID]
	for i := range result.Services {
		if stats, ok := m.processes[result.Services[i].PID]; ok {
			result.Services[i].Process = &stats
		}
	}
}
