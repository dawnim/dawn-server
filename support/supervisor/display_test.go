package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/prismgo/framework/console"
)

func TestDisplayFormats(t *testing.T) {
	for _, ansi := range []bool{false, true} {
		var out bytes.Buffer
		d := &Display{Out: &out, Locale: "zh", Options: console.OutputOptions{ANSI: ansi}}
		d.Log = "storage/framework/local-cli.log"
		d.Buffer()
		_, _ = d.Write([]byte("log after table\n"))
		threads, goroutines := int32(18), 21
		d.Render("服务状态", Response{PID: os.Getpid(), TerminalPID: 100, Services: []Status{
			{Name: "ws", State: "running", PID: 123, Address: "ws://localhost:8851"},
			{Name: "http", State: "running", PID: 124, Address: "http://localhost:5123", Process: &ProcessStats{User: "ieras", Threads: &threads, Goroutines: &goroutines}},
			{Name: "horizon", State: "running", PID: 125, Process: &ProcessStats{PID: 125, Name: "HORIZON", Children: []ProcessStats{{Name: "multiples"}, {Name: "supervisor-1"}}}},
			{Name: "cron", State: "waiting", PID: 127, LastError: "support_sessions.offline_since"},
			{Name: "frontend", State: "running", PID: 126, Address: "http://localhost:5173"},
		}})
		s := out.String()
		if strings.Contains(s, "\x1b[") != ansi || !strings.Contains(s, "等待就绪") || strings.Index(s, "HTTP") > strings.Index(s, "WS") || !strings.HasSuffix(s, "log after table\n") {
			t.Fatal(s)
		}
		if strings.Index(s, "用户") > strings.Index(s, "PID") || strings.Contains(s, "PPID") || strings.Index(s, "storage/framework/local-cli.log") > strings.Index(s, "进程") || !strings.Contains(s, "18 / 21") || !strings.Contains(s, "multiples") || !strings.Contains(s, "supervisor-1") || strings.Contains(s, "运行时长") {
			t.Fatal(s)
		}
		for _, address := range []string{"http://localhost:5123", "ws://localhost:8851", "http://localhost:5173"} {
			if strings.Count(s, address) != 1 {
				t.Fatal(s)
			}
		}
	}
	var out bytes.Buffer
	d := &Display{Out: &out, JSON: true}
	d.Render("服务状态", Response{PID: 1})
	var response Response
	if err := json.Unmarshal(out.Bytes(), &response); err != nil || response.PID != 1 {
		t.Fatal(out.String(), err)
	}
}

func TestProcessMetrics(t *testing.T) {
	m := &monitor{}
	m.record(RuntimeStats{PID: os.Getpid(), Goroutines: 7})
	r := Response{PID: os.Getpid()}
	m.enrich(context.Background(), &r)
	if r.Manager.PID != os.Getpid() || r.Manager.RSS == nil || r.Manager.Goroutines == nil || *r.Manager.Goroutines <= 0 {
		t.Fatal(r.Manager)
	}
	m.mu.Lock()
	m.at = time.Time{}
	m.mu.Unlock()
	m.enrich(context.Background(), &r)
	if r.Manager.CPU == nil || *r.Manager.CPU < 0 {
		t.Fatal("CPU unavailable after second sample", r.Manager)
	}
}
