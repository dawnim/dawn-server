package supervisor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dawnim/dawn-server/support/i18n"
	"github.com/prismgo/framework/config"
	"github.com/prismgo/framework/console"
	"golang.org/x/text/width"
)

// Display 是Dawn命令共用的终端输出，串行写入状态表与服务日志。
type Display struct {
	Out         io.Writer
	Options     console.OutputOptions
	Locale      string
	Environment string
	JSON        bool
	Log         string
	mu          sync.Mutex
	pending     bytes.Buffer
	buffering   bool
}

// NewDisplay 复用Console的输出目标、语言和终端选项。
func NewDisplay(output console.IO, locale string, json bool) *Display {
	return &Display{Out: console.OutputWriter(output), Options: console.OutputOptionsForIO(output), Locale: locale, Environment: config.GetString("app.env", "local"), JSON: json}
}

func (d *Display) Write(p []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.Options.Quiet || d.Options.Silent || d.JSON {
		return len(p), nil
	}
	if d.buffering {
		return d.pending.Write(p)
	}
	return d.Out.Write(p)
}

func (d *Display) Buffer() { d.mu.Lock(); d.buffering = true; d.mu.Unlock() }

func (d *Display) Flush() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.buffering = false
	_, _ = d.pending.WriteTo(d.Out)
}

func (d *Display) color(value, code string) string {
	if d.Options.ANSI {
		return "\x1b[" + code + "m" + value + "\x1b[0m"
	}
	return value
}

func textWidth(value string) int {
	n := 0
	for _, r := range value {
		switch width.LookupRune(r).Kind() {
		case width.EastAsianWide, width.EastAsianFullwidth:
			n += 2
		default:
			n++
		}
	}
	return n
}

// Heading 与 Result 供安装、模型检查和GeoIP等维护命令共用。
func (d *Display) Heading(title string) {
	_, _ = io.WriteString(d, d.header(title))
}

func (d *Display) header(title string) string {
	return "\n" + d.color("Dawn", "1;36") + " · " + d.color(i18n.Text(d.Locale, title), "1;37") + "  " + d.color(d.Environment+" · "+runtime.Version()+" · "+runtime.GOOS+"/"+runtime.GOARCH, "90") + "\n"
}

func (d *Display) Result(start time.Time, err error) {
	key, color := "已完成", "32"
	if err != nil {
		key, color = "执行失败", "31"
	}
	_, _ = fmt.Fprintf(d, "\n%s  %s\n", d.color(i18n.Text(d.Locale, key), color), time.Since(start).Round(time.Millisecond))
}

func (d *Display) Render(title string, response Response) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.Options.Silent || d.Options.Quiet {
		return
	}
	if d.JSON {
		_ = json.NewEncoder(d.Out).Encode(response)
		return
	}
	var output strings.Builder
	output.WriteString(d.header(title))
	if response.TerminalPID > 0 || d.Log != "" {
		output.WriteString("  ")
		if response.TerminalPID > 0 {
			output.WriteString(i18n.Text(d.Locale, "Ctrl+C停止当前终端管理的服务"))
		}
		if d.Log != "" {
			if response.TerminalPID > 0 {
				output.WriteString("  ·  ")
			}
			fmt.Fprintf(&output, "%s  %s", i18n.Text(d.Locale, "日志"), d.Log)
		}
		output.WriteByte('\n')
	}
	output.WriteByte('\n')
	columns := []string{"进程", "用户", "PID", "内存RSS", "CPU", "线程/协程", "运行/重启", "状态"}
	for i, key := range columns {
		columns[i] = i18n.Text(d.Locale, key)
	}
	rows := [][]string{columns}
	states := []string{""}
	var add func(ProcessStats, string, string, string, string)
	add = func(p ProcessStats, name, state, restarts, prefix string) {
		user, pid, rss, cpu, counts, uptime := p.User, "", "", "", "", ""
		if p.PID > 0 {
			pid = fmt.Sprint(p.PID)
		}
		if p.RSS != nil {
			rss = fmt.Sprintf("%.1f MiB", float64(*p.RSS)/(1<<20))
		}
		if p.CPU != nil {
			cpu = fmt.Sprintf("%.1f%%", *p.CPU)
		}
		if p.Threads != nil {
			counts = fmt.Sprint(*p.Threads)
		}
		if p.Goroutines != nil {
			if counts != "" {
				counts += " / "
			}
			counts += fmt.Sprint(*p.Goroutines)
		}
		if p.StartedAt > 0 {
			uptime = time.Since(time.UnixMilli(p.StartedAt)).Round(time.Second).String()
			if restarts != "" {
				uptime += " / " + restarts
			}
		}
		row := []string{name, user, pid, rss, cpu, counts, uptime, i18n.Text(d.Locale, state)}
		rows = append(rows, row)
		states = append(states, state)
		for i, child := range p.Children {
			branch, indent := "├─ ", "│  "
			if i == len(p.Children)-1 {
				branch, indent = "└─ ", "   "
			}
			add(child, prefix+branch+child.Name, "运行中", "", prefix+indent)
		}
	}
	manager := response.Manager
	if manager.PID == 0 {
		manager.PID = response.PID
	}
	add(manager, "Manager", "运行中", "", "")
	services := slices.Clone(response.Services)
	order := []string{"http", "ws", "horizon", "cron", "frontend", "embedding"}
	slices.SortStableFunc(services, func(a, b Status) int { return slices.Index(order, a.Name) - slices.Index(order, b.Name) })
	for i, service := range services {
		p := ProcessStats{PID: service.PID}
		if service.Process != nil {
			p = *service.Process
		}
		branch, indent := "├─ ", "│  "
		if i == len(services)-1 {
			branch, indent = "└─ ", "   "
		}
		add(p, branch+strings.ToUpper(service.Name), stateLabels[service.State], fmt.Sprint(service.Restarts), indent)
	}
	widths := make([]int, len(columns))
	for _, row := range rows {
		for j, value := range row {
			widths[j] = max(widths[j], textWidth(value))
		}
	}
	for i, row := range rows {
		for j, value := range row {
			padding := strings.Repeat(" ", widths[j]-textWidth(value)+2)
			if i == 0 {
				value = d.color(value, "90")
			} else if j == len(row)-1 {
				code := "90"
				switch states[i] {
				case "运行中":
					code = "32"
				case "启动中", "等待就绪", "等待重试", "停止中":
					code = "33"
				}
				value = d.color(value, code)
			}
			output.WriteString(value)
			if j < len(row)-1 {
				output.WriteString(padding)
			}
		}
		output.WriteByte('\n')
	}
	for _, service := range services {
		if service.LastError != "" {
			fmt.Fprintf(&output, "\n%s  %s\n", d.color(strings.ToUpper(service.Name), "33"), service.LastError)
		}
	}
	if response.Error != "" {
		fmt.Fprintf(&output, "\n%s\n", d.color(strings.TrimSpace(response.Error), "31"))
	}
	for _, service := range services {
		if service.State == "running" && service.Address != "" {
			fmt.Fprintf(&output, "%s  %s\n", strings.ToUpper(service.Name), service.Address)
		}
	}
	output.WriteByte('\n')
	_, _ = io.WriteString(d.Out, output.String())
	d.buffering = false
	_, _ = d.pending.WriteTo(d.Out)
}

type logWriter struct {
	mu            sync.Mutex
	out           io.Writer
	name, pending string
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending += string(p)
	for {
		line, rest, found := strings.Cut(w.pending, "\n")
		if !found {
			break
		}
		w.pending = rest
		if line != "" {
			if _, err := fmt.Fprintf(w.out, "%s  %-9s │ %s\n", time.Now().Format("15:04:05"), strings.ToUpper(w.name), line); err != nil {
				return 0, err
			}
		}
	}
	return len(p), nil
}
func (w *logWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pending != "" {
		_, _ = fmt.Fprintf(w.out, "%s  %-9s │ %s\n", time.Now().Format("15:04:05"), strings.ToUpper(w.name), w.pending)
		w.pending = ""
	}
}
