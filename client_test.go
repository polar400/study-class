package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func reply(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(Object{"code": 200, "data": data})
}
func progressReply(w http.ResponseWriter, p float64) {
	completed := 0
	if p >= 100 {
		completed = 1
	}
	reply(w, Object{"summary": Object{"totalNodes": 1, "completedNodes": completed}, "nodeProgressList": []Object{{"nodeId": 10, "progressPercent": p, "progressRatio": p / 100}}})
}
func testClient(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	s := httptest.NewTLSServer(h)
	t.Cleanup(s.Close)
	c := newClient(Account{Label: "test", BaseURL: s.URL, Username: "testuser", Password: "testpass"}, Global{Timeout: 5}, "")
	c.HTTP.Transport = s.Client().Transport
	c.School = "1"
	c.User = "2"
	return c, s
}
func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, e := url.Parse(s)
	if e != nil {
		t.Fatal(e)
	}
	return u
}

func TestCredentialIsolationAndRotation(t *testing.T) {
	var wrong atomic.Bool
	c, s := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token-a" {
			wrong.Store(true)
		}
		w.Header().Set("x-new-token", "rotated-a")
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "cookie-a"})
		reply(w, "ok")
	})
	d := newClient(Account{BaseURL: s.URL, Username: "other", Authorization: "token-b"}, Global{Timeout: 5}, "")
	d.HTTP.Transport = s.Client().Transport
	c.token = "token-a"
	if _, e := c.raw(context.Background(), "GET", "/test", nil, nil); e != nil {
		t.Fatal(e)
	}
	if wrong.Load() || c.token != "rotated-a" || d.token != "token-b" {
		t.Fatal("token isolation/rotation failed")
	}
	if len(d.HTTP.Jar.Cookies(mustURL(t, s.URL))) != 0 {
		t.Fatal("cookie crossed accounts")
	}
}

func TestPaginationRejectsRepeatedPage(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Object{"code": 200, "count": 2, "data": []Object{{"id": 1}}})
	})
	if _, e := c.courses(context.Background(), 1); e == nil {
		t.Fatal("repeated page accepted")
	}
}

func TestCheckModeDoesNotWrite(t *testing.T) {
	var writes atomic.Int32
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			writes.Add(1)
		}
		if r.URL.Path == "/api/user/get_study_progress" {
			progressReply(w, 0)
			return
		}
		reply(w, []Object{{"id": 1, "children": []Object{{"id": 10, "name": "video", "tabVideo": 1}, {"id": 11, "tabWork": 1}}}})
	})
	r := processCourse(context.Background(), c, Object{"id": 1}, Global{}, false, realClock{}, func(NodeResult) {})
	if r.Status != "checked" || len(r.Nodes) != 2 || writes.Load() != 0 {
		t.Fatalf("unexpected check result: %+v writes=%d", r, writes.Load())
	}
}

type fakeClock struct {
	now    time.Time
	cancel context.CancelFunc
}

func (f *fakeClock) Now() time.Time { return f.now }
func (f *fakeClock) Wait(ctx context.Context, d time.Duration) error {
	if f.cancel != nil {
		f.cancel()
		return ctx.Err()
	}
	f.now = f.now.Add(d)
	return nil
}

func TestStudyUsesElapsedTimeAndConfirmsAfterEnd(t *testing.T) {
	var events []string
	var values []string
	ended := false
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		events = append(events, r.URL.Path)
		switch r.URL.Path {
		case "/api/user/get_study_progress":
			if ended {
				progressReply(w, 100)
			} else {
				progressReply(w, 0)
			}
		case "/api/user/study_session_start":
			reply(w, "s1")
		case "/api/user/study_session_heartbeat":
			var b Object
			_ = json.NewDecoder(r.Body).Decode(&b)
			values = append(values, str(b["progress"]))
			reply(w, nil)
		case "/api/user/study_session_end":
			ended = true
			reply(w, nil)
		default:
			t.Error("unexpected endpoint")
			reply(w, nil)
		}
	})
	clock := &fakeClock{now: time.Unix(0, 0)}
	p, e := studyNode(context.Background(), c, "1", Object{"id": 10, "videoDuration": 60}, Global{Heartbeat: 30}, clock, func(float64) {})
	if e != nil || p != 100 || clock.Now().Sub(time.Unix(0, 0)) != time.Minute || strings.Join(values, ",") != "50,100" {
		t.Fatalf("elapsed/confirmation failed: %v p=%v values=%v", e, p, values)
	}
	if events[len(events)-2] != "/api/user/study_session_end" || events[len(events)-1] != "/api/user/get_study_progress" {
		t.Fatal("confirmation must occur after session end")
	}
}
func TestStudyDoesNotClaimUnconfirmedCompletion(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/get_study_progress":
			progressReply(w, 0)
		case "/api/user/study_session_start":
			reply(w, "s")
		default:
			reply(w, nil)
		}
	})
	_, e := studyNode(context.Background(), c, "1", Object{"id": 10, "videoDuration": 1}, Global{Heartbeat: 30}, &fakeClock{}, func(float64) {})
	if e == nil || !strings.Contains(e.Error(), "未确认完成") {
		t.Fatalf("false completion: %v", e)
	}
}
func TestCancellationClosesSession(t *testing.T) {
	var ends atomic.Int32
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/get_study_progress":
			progressReply(w, 0)
		case "/api/user/study_session_start":
			reply(w, "s")
		case "/api/user/study_session_end":
			ends.Add(1)
			reply(w, nil)
		default:
			reply(w, nil)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, e := studyNode(ctx, c, "1", Object{"id": 10, "videoDuration": 60}, Global{Heartbeat: 30}, &fakeClock{cancel: cancel}, func(float64) {})
	if !errors.Is(e, context.Canceled) || ends.Load() != 1 {
		t.Fatalf("cancel/end failed: %v ends=%d", e, ends.Load())
	}
}
func TestWriteAuthFailureIsNotReplayed(t *testing.T) {
	var calls atomic.Int32
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(401) })
	_, e := c.request(context.Background(), "POST", "/user/study_session_start", nil, Object{}, false)
	if !errors.Is(e, ErrAuth) || calls.Load() != 1 || !c.paused {
		t.Fatal("write replayed or account not paused")
	}
}
func TestAlreadyCompleteDoesNotStartSession(t *testing.T) {
	var calls atomic.Int32
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/user/get_study_progress" {
			t.Error("unexpected write")
		}
		progressReply(w, 100)
	})
	p, e := studyNode(context.Background(), c, "1", Object{"id": 10}, Global{}, &fakeClock{}, func(float64) {})
	if e != nil || p != 100 || calls.Load() != 1 {
		t.Fatal("complete node was started")
	}
}
func TestAuthenticationFailureDoesNotExposePassword(t *testing.T) {
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "yee_student_info") {
			w.WriteHeader(401)
			return
		}
		_ = json.NewEncoder(w).Encode(Object{"code": 500, "msg": "error testpass echoed"})
	})
	e := c.authenticate(context.Background(), false)
	if e == nil || strings.Contains(e.Error(), "testpass") {
		t.Fatalf("unsafe error: %v", e)
	}
}

func TestEncryptedSessionRoundTrip(t *testing.T) {
	dir := t.TempDir()
	a := Account{BaseURL: "https://example.test", Username: "one"}
	want := Session{Token: "test-token-secret", Cookies: []*http.Cookie{{Name: "session", Value: "test-cookie-secret", Path: "/"}}}
	if e := writeSession(dir, a, want); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(sessionPath(dir, a))
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(b), want.Token) || strings.Contains(string(b), want.Cookies[0].Value) {
		t.Fatal("session saved in plaintext")
	}
	got, e := readSession(dir, a)
	if e != nil || got.Token != want.Token || got.Cookies[0].Value != want.Cookies[0].Value {
		t.Fatal("DPAPI round trip failed", e)
	}
}

func TestReferenceConfigStaysUnchangedAndFiltersHosts(t *testing.T) {
	dir := t.TempDir()
	reference := filepath.Join(dir, "reference.json")
	source := `{"global":{"limit":99999},"users":[{"base_url":"https://allowed.test","username":"a"},{"base_url":"https://other.test","username":"b"}]}`
	if e := os.WriteFile(reference, []byte(source), 0600); e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(Object{"accounts_file": reference, "import_hosts": []string{"allowed.test"}})
	path := filepath.Join(dir, "config.json")
	_ = os.WriteFile(path, b, 0600)
	c, e := loadConfig(path)
	after, _ := os.ReadFile(reference)
	if e != nil || len(c.Users) != 1 || c.Global.Limit != 3 || string(after) != source {
		t.Fatalf("reference config changed or imported wrong scope: %v", e)
	}
}
func TestExpiredDoesNotMeanComplete(t *testing.T) {
	now := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	ok, why := eligible(Object{"id": 1, "endDate": "2026-01-01"}, Global{}, now)
	if ok || !strings.Contains(why, "不是已学完") {
		t.Fatal("expiry confused with completion")
	}
}
func TestDurationFormatsAndUnknownUnits(t *testing.T) {
	for _, s := range []string{"60", "01:00", "00:01:00"} {
		v, e := durationSeconds(s)
		if e != nil || v != 60 {
			t.Fatal(s, v, e)
		}
	}
	for _, s := range []string{"", "NaN", "-1", "600000", "1:90"} {
		if _, e := durationSeconds(s); e == nil {
			t.Fatal("accepted invalid duration", s)
		}
	}
}

func TestGlobalAndPerAccountConcurrency(t *testing.T) {
	var mu sync.Mutex
	active := 0
	max := 0
	per := map[string]int{}
	perMax := 0
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/api/course/selectdomain":
			reply(w, Object{"id": 1})
		case "/api/user/yee_student_info":
			reply(w, Object{"id": token, "number": token})
		case "/api/user/yee_my_course_list":
			_ = json.NewEncoder(w).Encode(Object{"code": 200, "count": 3, "data": []Object{{"id": 1}, {"id": 2}, {"id": 3}}})
		case "/api/user/get_study_progress":
			progressReply(w, 0)
		case "/api/user/yee_node_select":
			mu.Lock()
			active++
			per[token]++
			if active > max {
				max = active
			}
			if per[token] > perMax {
				perMax = per[token]
			}
			mu.Unlock()
			time.Sleep(20 * time.Millisecond)
			mu.Lock()
			active--
			per[token]--
			mu.Unlock()
			reply(w, []Object{})
		default:
			t.Error("unexpected endpoint", r.URL.Path)
			reply(w, nil)
		}
	}))
	defer s.Close()
	// 测试runAccount使用固定传输，局部信任本测试服务器证书，生产仍使用标准TLS校验。
	old := http.DefaultTransport
	http.DefaultTransport = s.Client().Transport
	defer func() { http.DefaultTransport = old }()
	dir := t.TempDir()
	m := &Monitor{dir: dir}
	global := make(chan struct{}, 2)
	g := Global{Limit: 2, PerAccount: 1, PageSize: 32, Timeout: 5}
	var wg sync.WaitGroup
	for _, name := range []string{"one", "two"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			runAccount(context.Background(), Account{Label: name, BaseURL: s.URL, Username: name, Authorization: name}, g, false, global, m, dir)
		}(name)
	}
	wg.Wait()
	if max != 2 || perMax != 1 || len(m.Report.Courses) != 6 {
		t.Fatalf("concurrency incorrect global=%d per=%d results=%d", max, perMax, len(m.Report.Courses))
	}
}

func TestStatusServerStopOnlyViaLocalPost(t *testing.T) {
	m := &Monitor{dir: t.TempDir()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, e := startServer("127.0.0.1:0", m, cancel)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	base := "http://" + s.Addr
	get, e := http.Get(base + "/stop")
	if e != nil {
		t.Fatal(e)
	}
	get.Body.Close()
	if get.StatusCode != 405 {
		t.Fatal("GET stopped server")
	}
	bad, _ := http.NewRequest("POST", base+"/stop", nil)
	bad.Header.Set("Origin", "https://other.test")
	resp, e := http.DefaultClient.Do(bad)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("cross-origin stop accepted")
	}
	resp, e = http.Post(base+"/stop", "text/plain", nil)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("local stop failed")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("cancel not signaled")
	}
	if _, e = startServer("0.0.0.0:10087", m, cancel); e == nil {
		t.Fatal("nonloopback allowed")
	}
}

func TestSameAccountTwoCoursesCanEnterWorkers(t *testing.T) {
	var active, max atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/course/selectdomain":
			reply(w, Object{"id": 1})
		case "/api/user/yee_student_info":
			reply(w, Object{"id": 2, "number": "one"})
		case "/api/user/yee_my_course_list":
			_ = json.NewEncoder(w).Encode(Object{"code": 200, "count": 2, "data": []Object{{"id": 1}, {"id": 2}}})
		case "/api/user/get_study_progress":
			progressReply(w, 0)
		case "/api/user/yee_node_select":
			n := active.Add(1)
			for {
				old := max.Load()
				if n <= old || max.CompareAndSwap(old, n) {
					break
				}
			}
			time.Sleep(25 * time.Millisecond)
			active.Add(-1)
			reply(w, []Object{})
		default:
			t.Error("unexpected endpoint")
			reply(w, nil)
		}
	}))
	defer s.Close()
	old := http.DefaultTransport
	http.DefaultTransport = s.Client().Transport
	defer func() { http.DefaultTransport = old }()
	dir := t.TempDir()
	m := &Monitor{dir: dir}
	runAccount(context.Background(), Account{Label: "one", BaseURL: s.URL, Username: "one", Authorization: "a"}, Global{Limit: 99999, PerAccount: 99999, PageSize: 32, Timeout: 5}, false, make(chan struct{}, 99999), m, dir)
	if max.Load() != 2 || len(m.Report.Courses) != 2 {
		t.Fatalf("per-account parallel courses failed: max=%d", max.Load())
	}
}

func TestConcurrencyConfigAcceptsLargePositiveValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	for _, n := range []int{9999, 99999, 0, -1} {
		b, _ := json.Marshal(Object{"global": Object{"limit": n, "per_account_limit": n}, "users": []Object{{"base_url": "https://example.test", "username": "test"}}})
		if e := os.WriteFile(path, b, 0600); e != nil {
			t.Fatal(e)
		}
		c, e := loadConfig(path)
		if n > 0 {
			if e != nil || c.Global.Limit != n || c.Global.PerAccount != n {
				t.Fatalf("positive concurrency %d rejected: %v", n, e)
			}
		} else if e == nil {
			t.Fatalf("non-positive concurrency %d accepted", n)
		}
	}
}

func TestConcurrentReadExpiryRefreshesOnce(t *testing.T) {
	var expired, logins atomic.Int32
	gate := make(chan struct{})
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/read":
			if r.Header.Get("Authorization") != "fresh" {
				if expired.Add(1) == 2 {
					close(gate)
				}
				<-gate
				w.WriteHeader(401)
				return
			}
			reply(w, "ok")
		case "/api/user/login":
			logins.Add(1)
			reply(w, "fresh")
		case "/api/user/yee_student_info":
			reply(w, Object{"id": 2, "number": "testuser"})
		default:
			t.Error("unexpected auth request")
			reply(w, nil)
		}
	})
	c.token = "expired"
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := c.request(context.Background(), "GET", "/read", nil, nil, true)
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if logins.Load() != 1 {
		t.Fatalf("concurrent auth refreshed %d times", logins.Load())
	}
}

func TestCompletedCourseSkipsBeforeChaptersOrStudy(t *testing.T) {
	var calls atomic.Int32
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" || r.URL.Path != "/api/user/get_study_progress" {
			t.Error("completed course should only read summary")
		}
		progressReply(w, 100)
	})
	r := processCourse(context.Background(), c, Object{"id": 1, "courseName": "completed"}, Global{}, true, &fakeClock{}, func(NodeResult) { t.Error("no node task expected") })
	if r.Status != "skipped_complete" || r.Progress == nil || *r.Progress != 100 || calls.Load() != 1 {
		t.Fatalf("completed course not skipped: %+v", r)
	}
}

func TestExplicitProgressUnitsDistinguishOnePercentFromComplete(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  Object
		want float64
	}{{"ratio one means complete", Object{"nodeId": 10, "progressRatio": "1.00"}, 100}, {"explicit one percent", Object{"nodeId": 10, "progressPercent": 1, "progressRatio": "0.01"}, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				reply(w, Object{"summary": Object{"totalNodes": 1, "completedNodes": 0}, "nodeProgressList": []Object{tc.row}})
			})
			p, e := c.progress(context.Background(), "1", "10")
			if e != nil || p != tc.want {
				t.Fatalf("progress=%v want=%v err=%v", p, tc.want, e)
			}
		})
	}
}

func TestFirstUnfinishedFailureDoesNotStartLaterLessons(t *testing.T) {
	var starts []string
	var displayed []string
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/get_study_progress":
			reply(w, Object{"summary": Object{"totalNodes": 3, "completedNodes": 1}, "nodeProgressList": []Object{{"nodeId": 10, "progressPercent": 100}, {"nodeId": 11, "progressPercent": 25}, {"nodeId": 12, "progressPercent": 0}}})
		case "/api/user/yee_node_select":
			reply(w, []Object{{"id": 5, "sort": 1, "children": []Object{{"id": 12, "sort": 3, "name": "5.5.2", "tabVideo": 1, "videoDuration": 60}, {"id": 10, "sort": 1, "name": "5.4.3", "tabVideo": 1, "videoDuration": 60}, {"id": 11, "sort": 2, "name": "5.5.1", "tabVideo": 1, "videoDuration": 60}}}})
		case "/api/user/study_session_start":
			var b Object
			_ = json.NewDecoder(r.Body).Decode(&b)
			starts = append(starts, str(b["nodeId"]))
			reply(w, "s")
		case "/api/user/study_session_heartbeat":
			_ = json.NewEncoder(w).Encode(Object{"code": 500})
		case "/api/user/study_session_end":
			reply(w, nil)
		default:
			t.Error("unexpected request", r.URL.Path)
		}
	})
	r := processCourse(context.Background(), c, Object{"id": 1}, Global{Heartbeat: 30}, true, &fakeClock{}, func(n NodeResult) { displayed = append(displayed, n.ID) })
	for _, id := range displayed {
		if id != "11" {
			t.Fatalf("displayed a completed or later lesson: %v", displayed)
		}
	}
	if r.Status != "paused" || strings.Join(starts, ",") != "11" || len(displayed) < 2 {
		t.Fatalf("scanned past unfinished lesson: status=%s starts=%v displayed=%v", r.Status, starts, displayed)
	}
}

func TestCourseAdvancesOnlyAfterPreviousPlatformConfirmation(t *testing.T) {
	completed := false
	active := ""
	var starts []string
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/get_study_progress":
			p := 0
			if completed {
				p = 100
			}
			done := 0
			if completed {
				done = 1
			}
			reply(w, Object{"summary": Object{"totalNodes": 2, "completedNodes": done}, "nodeProgressList": []Object{{"nodeId": 10, "progressPercent": p}, {"nodeId": 11, "progressPercent": 0}}})
		case "/api/user/yee_node_select":
			reply(w, []Object{{"id": 1, "children": []Object{{"id": 10, "tabVideo": 1, "videoDuration": 2}, {"id": 11, "tabVideo": 1, "videoDuration": 2}}}})
		case "/api/user/study_session_start":
			var b Object
			_ = json.NewDecoder(r.Body).Decode(&b)
			active = str(b["nodeId"])
			starts = append(starts, active)
			if active == "11" {
				if !completed {
					t.Error("next lesson started before confirmation")
				}
				_ = json.NewEncoder(w).Encode(Object{"code": 500})
				return
			}
			reply(w, "s")
		case "/api/user/study_session_heartbeat":
			reply(w, nil)
		case "/api/user/study_session_end":
			completed = true
			reply(w, nil)
		default:
			t.Error("unexpected request", r.URL.Path)
		}
	})
	r := processCourse(context.Background(), c, Object{"id": 1}, Global{Heartbeat: 30}, true, &fakeClock{}, func(NodeResult) {})
	if strings.Join(starts, ",") != "10,11" || r.Status != "paused" {
		t.Fatalf("unexpected sequential result starts=%v status=%s", starts, r.Status)
	}
}

func TestProgressDisplayUsesOnlyPlatformWholePercentChanges(t *testing.T) {
	beat := -1
	ended := false
	midReads := 0
	var displayed []int
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/get_study_progress":
			p := 0.0
			if ended {
				p = 100
			} else if beat >= 60 {
				p = 23
			} else if beat >= 30 {
				midReads++
				p = 20.2
				if midReads > 1 {
					p = 20.8
				}
			}
			progressReply(w, p)
		case "/api/user/study_session_start":
			reply(w, "s")
		case "/api/user/study_session_heartbeat":
			var b Object
			_ = json.NewDecoder(r.Body).Decode(&b)
			beat = integer(b["progress"])
			reply(w, nil)
		case "/api/user/study_session_end":
			ended = true
			reply(w, nil)
		default:
			t.Error("unexpected request", r.URL.Path)
		}
	})
	_, e := studyNode(context.Background(), c, "1", Object{"id": 10, "videoDuration": 100}, Global{Heartbeat: 30}, &fakeClock{}, func(p float64) { displayed = append(displayed, int(p)) })
	if e != nil || fmt.Sprint(displayed) != "[0 20 23 100]" {
		t.Fatalf("fabricated/repeated progress: %v err=%v", displayed, e)
	}
}

func TestProgressReadFailureRetainsLastConfirmedValue(t *testing.T) {
	reads := 0
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/get_study_progress":
			reads++
			if reads > 1 {
				_ = json.NewEncoder(w).Encode(Object{"code": 500})
				return
			}
			progressReply(w, 40)
		case "/api/user/study_session_start":
			reply(w, "s")
		default:
			reply(w, nil)
		}
	})
	p, e := studyNode(context.Background(), c, "1", Object{"id": 10, "videoDuration": 100}, Global{Heartbeat: 30}, &fakeClock{}, func(float64) {})
	if e == nil || p != 40 {
		t.Fatalf("last confirmed progress lost: progress=%v err=%v", p, e)
	}
}

func TestHeartbeatMessagesAreHiddenWhileConfirmedProgressIsPreserved(t *testing.T) {
	ended := false
	var observed []int
	var heartbeats []time.Duration
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/get_study_progress":
			if ended {
				progressReply(w, 100)
			} else {
				progressReply(w, 0)
			}
		case "/api/user/study_session_start":
			reply(w, "s")
		case "/api/user/study_session_end":
			ended = true
			reply(w, nil)
		default:
			reply(w, nil)
		}
	})
	p, e := studyNode(context.Background(), c, "1", Object{"id": 10, "videoDuration": 60}, Global{Heartbeat: 30}, &fakeClock{}, func(p float64) { observed = append(observed, int(p)) }, func(p float64, d time.Duration) {
		heartbeats = append(heartbeats, d)
		if p != 0 {
			t.Error("heartbeat must retain platform progress")
		}
		lines := nodeLogLines("account", "course", NodeResult{Name: "video", ID: "10", Status: "running", Event: "heartbeat", Progress: &p, Message: "心跳成功，平台进度暂未刷新"}, false)
		if len(lines) != 0 {
			t.Error("heartbeat/time messages must not be displayed")
		}
	})
	if e != nil || p != 100 || fmt.Sprint(observed) != "[0 100]" || fmt.Sprint(heartbeats) != "[30s 1m0s]" {
		t.Fatalf("heartbeat visibility failed: p=%v error=%v progress=%v heartbeat=%v", p, e, observed, heartbeats)
	}
}
