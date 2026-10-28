package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const version = "1.0.10"

type AccountResult struct {
	Label   string `json:"label"`
	Status  string `json:"status"`
	Courses int    `json:"courses"`
	Message string `json:"message,omitempty"`
}
type Report struct {
	Version  string          `json:"version"`
	Mode     string          `json:"mode"`
	Started  string          `json:"started"`
	Finished string          `json:"finished,omitempty"`
	Status   string          `json:"status"`
	Accounts []AccountResult `json:"accounts"`
	Courses  []CourseResult  `json:"courses"`
}
type Monitor struct {
	mu     sync.Mutex
	Report Report
	dir    string
}

func (m *Monitor) update(f func(*Report)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f(&m.Report)
	b, _ := json.MarshalIndent(m.Report, "", "  ")
	_ = os.WriteFile(filepath.Join(m.dir, "status.json.tmp"), b, 0600)
	_ = os.Rename(filepath.Join(m.dir, "status.json.tmp"), filepath.Join(m.dir, "status.json"))
}
func (m *Monitor) snapshot() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, _ := json.Marshal(m.Report)
	return b
}

func main() { os.Exit(entry()) }
func entry() int {
	mode := flag.String("mode", "check", "check=只读检查；study=按真实时间执行后台学习会话（尚待现场验证）")
	configPath := flag.String("config", "config.json", "程序配置路径")
	serve := flag.Bool("serve", true, "保留本地状态页；任务结束后仍可查看，停止后退出")
	interactive := flag.Bool("interactive", false, "允许在控制台手动输入验证码（后台隐藏运行不要启用）")
	preview := flag.Bool("preview", false, "仅预览POLAR标识与日志排版，不连接平台")
	flag.Parse()
	color, restoreConsole := enableConsoleColor(os.Stdout)
	defer restoreConsole()
	if *preview {
		printConsolePreview(os.Stdout, color)
		return 0
	}
	if *mode != "check" && *mode != "study" {
		fmt.Fprintln(consoleWriter{dst: os.Stdout, color: color}, consoleMessage("failed", "mode只支持check/study"))
		return 2
	}
	exe, _ := os.Executable()
	root := filepath.Dir(exe)
	if strings.HasPrefix(filepath.Base(exe), "go_build") || strings.Contains(root, "go-build") {
		root, _ = os.Getwd()
	}
	if !filepath.IsAbs(*configPath) {
		*configPath = filepath.Join(root, *configPath)
	}
	// 所有输出固定在新EXE目录；即使指定外部配置也只读，绝不在那里创建输出。
	for _, dir := range []string{"logs", "state", "reports"} {
		if e := os.MkdirAll(filepath.Join(root, dir), 0700); e != nil {
			fmt.Fprintln(consoleWriter{dst: os.Stdout, color: color}, consoleMessage("failed", "无法创建程序输出目录"))
			return 2
		}
	}
	f, e := os.OpenFile(filepath.Join(root, "logs", "xianshua.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		fmt.Fprintln(consoleWriter{dst: os.Stdout, color: color}, consoleMessage("failed", "无法创建日志"))
		return 2
	}
	defer f.Close()
	destination := io.MultiWriter(os.Stdout, f)
	fmt.Fprint(destination, polarBanner)
	log.SetOutput(io.MultiWriter(consoleWriter{dst: os.Stdout, color: color}, consoleWriter{dst: f}))
	log.SetFlags(0)
	cfg, e := loadConfig(*configPath)
	if e != nil {
		logStatusf("failed", "%v", e)
		return 2
	}
	cfg.Global.Interactive = *interactive
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	m := &Monitor{dir: filepath.Join(root, "reports"), Report: Report{Version: version, Mode: *mode, Started: time.Now().Format(time.RFC3339), Status: "running", Accounts: []AccountResult{}, Courses: []CourseResult{}}}
	m.update(func(*Report) {})
	var server *http.Server
	if *serve {
		server, e = startServer(cfg.Global.Server, m, cancel)
		if e != nil {
			logStatusf("failed", "%v", e)
			return 2
		}
		defer server.Close()
		log.Printf("状态页 http://%s（仅本机）", cfg.Global.Server)
	}
	log.Printf("先刷 v%s，模式=%s，账号=%d，全局并发=%d，每账号并发=%d", version, *mode, len(cfg.Users), cfg.Global.Limit, cfg.Global.PerAccount)
	if *mode == "study" {
		log.Print("学习模式：多课并行，课内按未完成视频顺序继续。")
	}
	global := make(chan struct{}, cfg.Global.Limit)
	var accounts sync.WaitGroup
	for _, a := range cfg.Users {
		a := a
		accounts.Add(1)
		go func() {
			defer accounts.Done()
			runAccount(ctx, a, cfg.Global, *mode == "study", global, m, filepath.Join(root, "state"))
		}()
	}
	accounts.Wait()
	exitCode := 0
	m.update(func(r *Report) {
		r.Finished = time.Now().Format(time.RFC3339)
		r.Status = "finished"
		for _, a := range r.Accounts {
			if a.Status == "failed" || a.Status == "paused" {
				r.Status = "needs_attention"
				exitCode = 1
			}
		}
		for _, c := range r.Courses {
			if c.Status == "failed" || c.Status == "paused" {
				r.Status = "needs_attention"
				exitCode = 1
			}
		}
		if ctx.Err() != nil {
			r.Status = "stopped"
			exitCode = 130
		}
	})
	finalStatus := func() string { m.mu.Lock(); defer m.mu.Unlock(); return m.Report.Status }()
	logStatusf(finalStatus, "本轮处理结束，结果=%s；详见 reports/status.json。", finalStatus)
	if server != nil && ctx.Err() == nil {
		log.Print("状态页继续运行；Ctrl+C或停止按钮退出。")
		<-ctx.Done()
	}
	return exitCode
}

func runAccount(ctx context.Context, a Account, g Global, study bool, global chan struct{}, m *Monitor, stateDir string) {
	// 限制认证和列表请求也进入全局并发预算。
	select {
	case global <- struct{}{}:
	case <-ctx.Done():
		return
	}
	c := newClient(a, g, stateDir)
	e := c.authenticate(ctx, false)
	if e != nil {
		<-global
		status := "failed"
		if errors.Is(e, ErrCaptcha) || errors.Is(e, ErrAuth) {
			status = "paused"
		}
		m.update(func(r *Report) {
			r.Accounts = append(r.Accounts, AccountResult{Label: a.Label, Status: status, Message: e.Error()})
		})
		logStatusf(status, "[%s] %s", a.Label, e)
		return
	}
	courses, e := c.courses(ctx, g.PageSize)
	<-global
	if e != nil {
		m.update(func(r *Report) {
			r.Accounts = append(r.Accounts, AccountResult{Label: a.Label, Status: "failed", Message: e.Error()})
		})
		logStatusf("failed", "[%s] 课程读取失败: %v", a.Label, e)
		return
	}
	m.update(func(r *Report) {
		r.Accounts = append(r.Accounts, AccountResult{Label: a.Label, Status: "authenticated", Courses: len(courses)})
	})
	log.Printf("[%s] 认证成功，课程共%d门", a.Label, len(courses))
	var pending []Object
	for _, course := range courses {
		ok, reason := eligible(course, g, time.Now())
		if !ok {
			m.update(func(r *Report) {
				r.Courses = append(r.Courses, CourseResult{Account: a.Label, ID: str(course["id"]), Name: field(course, "courseName", "name"), Status: "skipped", Message: reason})
			})
			continue
		}
		pending = append(pending, course)
	}
	workerCount := min(g.Limit, g.PerAccount, len(pending))
	log.Printf("[%s] 有效课程任务=%d，实际工作协程=%d", a.Label, len(pending), workerCount)
	jobs := make(chan Object)
	var workers sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for course := range jobs {
				select {
				case global <- struct{}{}:
				case <-ctx.Done():
					return
				}
				id := str(course["id"])
				name := field(course, "courseName", "name")
				m.update(func(r *Report) {
					r.Courses = append(r.Courses, CourseResult{Account: a.Label, ID: id, Name: name, Status: "running"})
				})
				log.Printf("[%s] 当前课程[%s][courseId=%s]", a.Label, cleanText(name), id)
				lastChapterIndex := 0
				result := processCourse(ctx, c, course, g, study, realClock{}, func(n NodeResult) {
					for _, line := range nodeLogLines(a.Label, name, n, n.ChapterIndex != lastChapterIndex) {
						logStatusf(n.Status, "%s", line)
					}
					if n.Status == "current" {
						lastChapterIndex = n.ChapterIndex
					}
					m.update(func(r *Report) {
						for i := range r.Courses {
							v := &r.Courses[i]
							if v.Account == a.Label && v.ID == id {
								updated := false
								for j := range v.Nodes {
									if v.Nodes[j].ID == n.ID {
										v.Nodes[j] = n
										updated = true
										break
									}
								}
								if !updated {
									v.Nodes = append(v.Nodes, n)
								}
								break
							}
						}
					})
				})
				m.update(func(r *Report) {
					for i, v := range r.Courses {
						if v.Account == a.Label && v.ID == id {
							r.Courses[i] = result
							break
						}
					}
				})
				if result.Status == "skipped_complete" {
					logStatusf(result.Status, "[%s] 当前课程[%s][%s] 进度: 已学习 100%%, 跳过", a.Label, cleanText(name), id)
				} else if result.Status == "videos_confirmed" {
					logStatusf(result.Status, "[%s] 当前课程[%s][%s] 视频进度: 已学习 100%%", a.Label, cleanText(name), id)
				} else if result.Status == "checked" {
					log.Printf("[%s] 当前课程[%s][%s] 检查完成", a.Label, cleanText(name), id)
				} else if result.Status != "cancelled" && (len(result.Nodes) == 0 || result.Nodes[len(result.Nodes)-1].Status != "failed") {
					logStatusf(result.Status, "[%s][课程%s] %s %s", a.Label, id, result.Status, cleanText(result.Message))
				}
				<-global
			}
		}()
	}
sendJobs:
	for _, course := range pending {
		select {
		case jobs <- course:
		case <-ctx.Done():
			break sendJobs
		}
	}
	close(jobs)
	workers.Wait()
	if e := c.save(); e != nil {
		logStatusf("failed", "[%s] 会话保存失败", a.Label)
	}
}
func cleanText(s string) string { return strings.NewReplacer("\r", " ", "\n", " ").Replace(s) }
func progressText(p *float64) string {
	if p == nil {
		return ""
	}
	return fmt.Sprintf(" %.1f%%", *p)
}

func startServer(addr string, m *Monitor, cancel context.CancelFunc) (*http.Server, error) {
	host, _, e := net.SplitHostPort(addr)
	if e != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return nil, errors.New("server必须为127.0.0.1等本机回环地址及端口")
	}
	l, e := net.Listen("tcp", addr)
	if e != nil {
		return nil, errors.New("状态端口无法监听，可能已有先刷程序运行")
	}
	addr = l.Addr().String()
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(m.snapshot())
	})
	mux.HandleFunc("/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
			w.WriteHeader(403)
			return
		}
		if r.Host != addr {
			w.WriteHeader(403)
			return
		}
		cancel()
		_, _ = w.Write([]byte("stopping"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, statusHTML)
	})
	s := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if e := s.Serve(l); e != nil && !errors.Is(e, http.ErrServerClosed) {
			log.Print("状态页服务停止")
			cancel()
		}
	}()
	return s, nil
}

const statusHTML = `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>先刷 · 运行状态</title><style>body{font:16px system-ui;background:#f5f6f8;color:#243044;max-width:1100px;margin:40px auto;padding:20px}button{padding:10px 18px;cursor:pointer}pre{background:white;padding:24px;border-radius:12px;white-space:pre-wrap;overflow-wrap:anywhere}</style><h1>先刷 · 运行状态</h1><p>后台会话的实际效果以平台回读为准。视频完成不代表作业、学分或结课达标。</p><button id="stop">停止本程序</button><pre id="data">读取中…</pre><script>document.querySelector('#stop').onclick=async()=>{await fetch('/stop',{method:'POST'});document.querySelector('#stop').disabled=true};async function refresh(){try{const r=await fetch('/status');document.querySelector('#data').textContent=JSON.stringify(await r.json(),null,2)}catch{document.querySelector('#data').textContent+='\n服务已停止'}setTimeout(refresh,2000)}refresh()</script></html>`
