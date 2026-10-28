package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ErrAuth = errors.New("登录状态失效")
var ErrCaptcha = errors.New("需要人工验证码；该账号已暂停")

type BusinessError struct{ Path, Code, Message string }

func (e *BusinessError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("%s: 业务响应未成功(code=%s): %s", e.Path, e.Code, e.Message)
	}
	return fmt.Sprintf("%s: 业务响应未成功(code=%s)", e.Path, e.Code)
}
func (c *Client) safeMessage(message string) string {
	c.mu.Lock()
	token := c.token
	c.mu.Unlock()
	for _, secret := range []string{c.Account.Password, c.Account.Authorization, c.Account.Cookie, token} {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[已隐藏]")
		}
	}
	message = strings.Join(strings.Fields(message), " ")
	r := []rune(message)
	if len(r) > 240 {
		message = string(r[:240]) + "…"
	}
	return message
}

type Object = map[string]any

type Global struct {
	Server       string   `json:"server"`
	Limit        int      `json:"limit"`
	PerAccount   int      `json:"per_account_limit"`
	PageSize     int      `json:"page_size"`
	Heartbeat    int      `json:"heartbeat_seconds"`
	Timeout      int      `json:"timeout_seconds"`
	CourseIDs    []string `json:"course_ids"`
	IncludeEnded bool     `json:"include_ended"`
	VerifyTLS    *bool    `json:"verify_tls,omitempty"`
	Interactive  bool     `json:"-"`
}
type Account struct {
	Label         string `json:"label,omitempty"`
	BaseURL       string `json:"base_url"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	Authorization string `json:"authorization,omitempty"`
	Cookie        string `json:"cookie,omitempty"`
	SchoolID      any    `json:"school_id,omitempty"`
	Enabled       *bool  `json:"enabled,omitempty"`
}
type Config struct {
	Global       Global    `json:"global"`
	Users        []Account `json:"users"`
	AccountsFile string    `json:"accounts_file,omitempty"`
	ImportHosts  []string  `json:"import_hosts,omitempty"`
}

func loadConfig(path string) (Config, error) {
	c := Config{Global: Global{Server: "127.0.0.1:10087", Limit: 3, PerAccount: 1, PageSize: 32, Heartbeat: 30, Timeout: 30}}
	b, err := os.ReadFile(path)
	if err != nil {
		return c, errors.New("无法读取程序配置")
	}
	if err = json.Unmarshal(b, &c); err != nil {
		return c, errors.New("程序配置不是有效JSON")
	}
	if c.AccountsFile != "" {
		p := c.AccountsFile
		if !filepath.IsAbs(p) {
			p = filepath.Join(filepath.Dir(path), p)
		}
		b, err = os.ReadFile(p)
		if err != nil {
			return c, errors.New("无法只读加载 accounts_file")
		}
		var external struct {
			Users []Account `json:"users"`
		}
		if json.Unmarshal(b, &external) != nil {
			return c, errors.New("参考账号文件不是有效JSON")
		}
		for _, a := range external.Users {
			u, e := url.Parse(a.BaseURL)
			if e != nil {
				continue
			}
			for _, host := range c.ImportHosts {
				if strings.EqualFold(u.Hostname(), host) {
					c.Users = append(c.Users, a)
					break
				}
			}
		}
	}
	if c.Global.Limit < 1 || c.Global.PerAccount < 1 {
		return c, errors.New("limit和per_account_limit必须为正整数")
	}
	if c.Global.PageSize < 1 || c.Global.PageSize > 100 || c.Global.Heartbeat < 10 || c.Global.Heartbeat > 60 || c.Global.Timeout < 1 || c.Global.Timeout > 300 {
		return c, errors.New("page_size需为1–100，heartbeat_seconds需为10–60，timeout_seconds需为1–300")
	}
	if c.Global.VerifyTLS != nil && !*c.Global.VerifyTLS {
		return c, errors.New("不支持关闭TLS证书校验")
	}
	seen := map[string]bool{}
	var users []Account
	for _, a := range c.Users {
		if a.Enabled != nil && !*a.Enabled {
			continue
		}
		u, e := url.Parse(strings.TrimSpace(a.BaseURL))
		if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return c, errors.New("base_url必须为不含路径的HTTPS站点地址")
		}
		a.BaseURL = strings.TrimRight(u.String(), "/")
		key := a.BaseURL + "\x00" + a.Username
		if seen[key] {
			continue
		}
		seen[key] = true
		a.Label = cleanText(a.Username)
		if a.Username == "" {
			return c, errors.New("账号缺少username")
		}
		users = append(users, a)
	}
	c.Users = users
	if len(users) == 0 {
		return c, errors.New("没有启用的账号，请检查users或accounts_file/import_hosts")
	}
	return c, nil
}

func str(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprint(v)
	}
}
func obj(v any) Object {
	if x, ok := v.(map[string]any); ok {
		return x
	}
	return Object{}
}
func arr(v any) []Object {
	a, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]Object, 0, len(a))
	for _, v := range a {
		if x, ok := v.(map[string]any); ok {
			out = append(out, x)
		}
	}
	return out
}
func integer(v any) int { n, _ := strconv.Atoi(str(v)); return n }
func field(o Object, keys ...string) string {
	for _, k := range keys {
		if v := str(o[k]); v != "" {
			return v
		}
	}
	return ""
}

type Client struct {
	Account     Account
	HTTP        *http.Client
	School      string
	User        string
	mu          sync.Mutex
	authMu      sync.Mutex
	token       string
	paused      bool
	stateDir    string
	interactive bool
}

func newClient(a Account, g Global, stateDir string) *Client {
	jar, _ := cookiejar.New(nil)
	c := &Client{Account: a, token: strings.TrimSpace(a.Authorization), stateDir: stateDir, interactive: g.Interactive}
	c.HTTP = &http.Client{Jar: jar, Timeout: time.Duration(g.Timeout) * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || req.URL.Host != via[0].URL.Host || req.URL.Scheme != "https" {
			return errors.New("拒绝跨站重定向")
		}
		return nil
	}}
	if cached, err := readSession(stateDir, a); err == nil {
		if cached.Token != "" {
			c.token = cached.Token
		}
		u, _ := url.Parse(a.BaseURL)
		jar.SetCookies(u, cached.Cookies)
	}
	if a.Cookie != "" {
		u, _ := url.Parse(a.BaseURL)
		var cookies []*http.Cookie
		for _, pair := range strings.Split(strings.TrimPrefix(a.Cookie, "Cookie:"), ";") {
			k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
			if ok && k != "" {
				cookies = append(cookies, &http.Cookie{Name: k, Value: v, Path: "/", Secure: true})
			}
		}
		jar.SetCookies(u, cookies)
	}
	return c
}

func (c *Client) raw(ctx context.Context, method, path string, q url.Values, data any) (Object, error) {
	u := c.Account.BaseURL + "/api" + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var body io.Reader
	ct := ""
	if data != nil {
		if form, ok := data.(url.Values); ok {
			body = strings.NewReader(form.Encode())
			ct = "application/x-www-form-urlencoded"
		} else {
			b, e := json.Marshal(data)
			if e != nil {
				return nil, errors.New("请求编码失败")
			}
			body = strings.NewReader(string(b))
			ct = "application/json"
		}
	}
	req, e := http.NewRequestWithContext(ctx, method, u, body)
	if e != nil {
		return nil, errors.New("无法创建请求")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "XianShua/1.0 Windows")
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	c.mu.Lock()
	token := c.token
	c.mu.Unlock()
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	resp, e := c.HTTP.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%s %s: 网络或TLS请求失败", method, path)
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, ErrAuth
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %s: HTTP %d", method, path, resp.StatusCode)
	}
	var out Object
	dec := json.NewDecoder(io.LimitReader(resp.Body, 8<<20))
	dec.UseNumber()
	if dec.Decode(&out) != nil || out == nil {
		return nil, fmt.Errorf("%s: 响应不是JSON对象", path)
	}
	for _, k := range []string{"x-new-token", "x_new_token"} {
		if t := resp.Header.Get(k); t != "" {
			c.mu.Lock()
			c.token = t
			c.mu.Unlock()
			break
		}
	}
	code := str(out["code"])
	if code == "401" || code == "403" {
		return nil, ErrAuth
	}
	if code != "200" && code != "0" {
		if out["needCode"] == true || strings.Contains(strings.ToLower(str(out["msg"])), "captcha") || strings.Contains(str(out["msg"]), "验证码") {
			return nil, ErrCaptcha
		}
		return nil, &BusinessError{Path: path, Code: safeCode(code), Message: c.safeMessage(str(out["msg"]))}
	}
	return out, nil
}
func safeCode(s string) string {
	if len(s) > 10 {
		return "未知"
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return "未知"
		}
	}
	if s == "" {
		return "缺失"
	}
	return s
}

func (c *Client) save() error {
	c.mu.Lock()
	t := c.token
	c.mu.Unlock()
	u, _ := url.Parse(c.Account.BaseURL)
	return writeSession(c.stateDir, c.Account, Session{Token: t, Cookies: c.HTTP.Jar.Cookies(u)})
}

func (c *Client) authenticate(ctx context.Context, force bool) error {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	return c.authenticateLocked(ctx, force)
}
func (c *Client) authenticateLocked(ctx context.Context, force bool) error {
	c.mu.Lock()
	paused := c.paused
	c.mu.Unlock()
	if paused {
		return ErrAuth
	}
	if c.School == "" {
		r, e := c.raw(ctx, "GET", "/course/selectdomain", url.Values{"domain": {mustHost(c.Account.BaseURL)}}, nil)
		if e != nil {
			return e
		}
		c.School = str(obj(r["data"])["id"])
		if c.School == "" {
			return errors.New("学校识别成功但缺少学校ID")
		}
	}
	if !force {
		if e := c.loadProfile(ctx); e == nil {
			return c.save()
		} else if !errors.Is(e, ErrAuth) {
			return e
		}
	}
	if c.Account.Password == "" {
		return ErrAuth
	}
	// 只尝试网站正常密码登录；验证码策略由服务器决定，不使用旧程序的固定验证码。
	r, e := c.raw(ctx, "GET", "/user/login", url.Values{"number": {c.Account.Username}, "password": {c.Account.Password}, "schoolId": {c.School}}, nil)
	if errors.Is(e, ErrCaptcha) && c.interactive {
		if e = c.solveCaptcha(ctx); e != nil {
			return e
		}
		r, e = c.raw(ctx, "GET", "/user/login", url.Values{"number": {c.Account.Username}, "password": {c.Account.Password}, "schoolId": {c.School}}, nil)
	}
	if e != nil {
		return e
	}
	t := r["data"]
	token := ""
	switch x := t.(type) {
	case string:
		token = x
	case map[string]any:
		token = field(x, "token", "accessToken", "access_token")
	}
	if token == "" {
		return errors.New("登录响应未包含可识别token；需要人工确认登录契约")
	}
	c.mu.Lock()
	c.token = token
	c.mu.Unlock()
	if e = c.loadProfile(ctx); e != nil {
		return e
	}
	return c.save()
}
func mustHost(base string) string { u, _ := url.Parse(base); return u.Hostname() }
func (c *Client) loadProfile(ctx context.Context) error {
	r, e := c.raw(ctx, "GET", "/user/yee_student_info", nil, nil)
	if e != nil {
		return e
	}
	p := obj(r["data"])
	if str(p["number"]) != c.Account.Username {
		return errors.New("会话账号与配置username不一致，已停止使用该会话")
	}
	id := field(p, "id", "userId")
	if id == "" {
		return errors.New("用户资料缺少ID")
	}
	if c.User == "" {
		c.User = id
	} else if c.User != id {
		return errors.New("重新认证后的用户ID改变，停止该账号")
	}
	return nil
}
func (c *Client) request(ctx context.Context, method, path string, q url.Values, data any, readOnly bool) (Object, error) {
	c.mu.Lock()
	paused := c.paused
	before := c.token
	c.mu.Unlock()
	if paused {
		return nil, ErrAuth
	}
	r, e := c.raw(ctx, method, path, q, data)
	if !errors.Is(e, ErrAuth) {
		return r, e
	}
	// 写请求不重放，避免不明确的学习会话重复创建。
	if !readOnly {
		c.pause()
		return nil, ErrAuth
	}
	c.authMu.Lock()
	c.mu.Lock()
	changed := c.token != before
	c.mu.Unlock()
	var refreshErr error
	if !changed {
		refreshErr = c.authenticateLocked(ctx, true)
	}
	c.authMu.Unlock()
	if refreshErr != nil {
		c.pause()
		return nil, refreshErr
	}
	r, e = c.raw(ctx, method, path, q, data)
	if errors.Is(e, ErrAuth) {
		c.pause()
	}
	return r, e
}
func (c *Client) pause() { c.mu.Lock(); c.paused = true; c.mu.Unlock() }

var captchaPromptMu sync.Mutex

func (c *Client) solveCaptcha(ctx context.Context) error {
	captchaPromptMu.Lock()
	defer captchaPromptMu.Unlock()
	r, e := c.raw(ctx, "GET", "/captcha", nil, nil)
	if e != nil {
		return e
	}
	imageText := str(r["data"])
	if _, v, ok := strings.Cut(imageText, ","); ok {
		imageText = v
	}
	b, e := base64.StdEncoding.DecodeString(imageText)
	if e != nil || len(b) == 0 {
		return errors.New("验证码图片格式无法识别")
	}
	dir := filepath.Join(filepath.Dir(c.stateDir), "logs", "captcha")
	if os.MkdirAll(dir, 0700) != nil {
		return errors.New("无法保存验证码图片")
	}
	path := filepath.Join(dir, c.Account.Label+".png")
	if os.WriteFile(path, b, 0600) != nil {
		return errors.New("无法保存验证码图片")
	}
	defer os.Remove(path)
	fmt.Printf("[%s] 请打开验证码图片 %s，输入验证码后回车（Ctrl+C停止）：", c.Account.Label, path)
	type answer struct {
		s string
		e error
	}
	done := make(chan answer, 1)
	go func() { s, e := bufio.NewReader(os.Stdin).ReadString('\n'); done <- answer{s, e} }()
	var code string
	select {
	case <-ctx.Done():
		return ctx.Err()
	case a := <-done:
		if a.e != nil {
			return ErrCaptcha
		}
		code = strings.TrimSpace(a.s)
	}
	if code == "" {
		return ErrCaptcha
	}
	_, e = c.raw(ctx, "POST", "/verify", nil, url.Values{"uuid": {str(r["msg"])}, "code": {code}})
	if e != nil {
		return ErrCaptcha
	}
	return nil
}

func (c *Client) courses(ctx context.Context, size int) ([]Object, error) {
	var all []Object
	seen := map[string]bool{}
	for page := 1; page <= 1000; page++ {
		r, e := c.request(ctx, "GET", "/user/yee_my_course_list", url.Values{"schoolId": {c.School}, "studentId": {c.User}, "type": {"0"}, "pageNum": {strconv.Itoa(page)}, "pageSize": {strconv.Itoa(size)}}, nil, true)
		if e != nil {
			return nil, e
		}
		rows := arr(r["data"])
		if rows == nil {
			rows = arr(obj(r["data"])["list"])
		}
		if rows == nil {
			rows = arr(r["list"])
		}
		if len(rows) == 0 {
			if integer(r["count"]) > len(all) {
				return nil, errors.New("课程分页返回空页且未达到总数")
			}
			return all, nil
		}
		for _, row := range rows {
			id := str(row["id"])
			if id == "" || seen[id] {
				return nil, errors.New("课程ID缺失或分页重复，停止收集")
			}
			seen[id] = true
			all = append(all, row)
		}
		total := integer(r["count"])
		if total > 0 && len(all) >= total {
			return all, nil
		}
		if total == 0 && len(rows) < size {
			return all, nil
		}
	}
	return nil, errors.New("课程分页超过1000页")
}
func (c *Client) chapters(ctx context.Context, course string) ([]Object, error) {
	r, e := c.request(ctx, "GET", "/user/yee_node_select", url.Values{"schoolId": {c.School}, "studentId": {c.User}, "courseId": {course}}, nil, true)
	if e != nil {
		return nil, e
	}
	rows := arr(r["data"])
	if rows == nil {
		rows = arr(obj(r["data"])["list"])
	}
	if rows == nil {
		return nil, errors.New("章节响应不是列表")
	}
	return rows, nil
}

type StudyProgress struct {
	Total     int
	Completed int
	Nodes     map[string]float64
}

func (c *Client) studyProgress(ctx context.Context, course string) (StudyProgress, error) {
	var out StudyProgress
	r, e := c.request(ctx, "GET", "/user/get_study_progress", url.Values{"schoolId": {c.School}, "userId": {c.User}, "courseId": {course}}, nil, true)
	if e != nil {
		return out, e
	}
	data := obj(r["data"])
	summary := obj(data["summary"])
	total, e := strconv.Atoi(str(summary["totalNodes"]))
	if e != nil || total < 0 {
		return out, errors.New("课程进度缺少有效totalNodes")
	}
	completed, e := strconv.Atoi(str(summary["completedNodes"]))
	if e != nil || completed < 0 || completed > total {
		return out, errors.New("课程进度completedNodes无法识别")
	}
	out.Total = total
	out.Completed = completed
	out.Nodes = map[string]float64{}
	for _, row := range arr(data["nodeProgressList"]) {
		id := str(row["nodeId"])
		if id == "" {
			continue
		}
		value := str(row["progressPercent"])
		scale := 1.0
		if value == "" {
			value = str(row["progressRatio"])
			scale = 100
		}
		v, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v*scale > 100 {
			return out, errors.New("节点进度百分比无法识别")
		}
		out.Nodes[id] = v * scale
	}
	return out, nil
}
func (c *Client) progress(ctx context.Context, course, node string) (float64, error) {
	s, e := c.studyProgress(ctx, course)
	if e != nil {
		return 0, e
	}
	v, ok := s.Nodes[node]
	if !ok {
		return 0, errors.New("平台课程进度中未返回该视频节点")
	}
	return v, nil
}
