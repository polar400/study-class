package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

type NodeResult struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Status        string   `json:"status"`
	Progress      *float64 `json:"progress,omitempty"`
	TimedProgress *float64 `json:"timed_progress,omitempty"`
	Message       string   `json:"message,omitempty"`
	ChapterID     string   `json:"chapter_id,omitempty"`
	ChapterName   string   `json:"chapter_name,omitempty"`
	ChapterIndex  int      `json:"chapter_index,omitempty"`
	LessonIndex   int      `json:"lesson_index,omitempty"`
	Event         string   `json:"-"`
}
type CourseResult struct {
	Account  string       `json:"account"`
	ID       string       `json:"id"`
	Name     string       `json:"name"`
	Status   string       `json:"status"`
	Progress *float64     `json:"progress,omitempty"`
	Message  string       `json:"message,omitempty"`
	Nodes    []NodeResult `json:"nodes,omitempty"`
}

func durationSeconds(v any) (float64, error) {
	s := strings.TrimSpace(str(v))
	if s == "" {
		return 0, errors.New("视频时长缺失")
	}
	var seconds float64
	if strings.Contains(s, ":") {
		p := strings.Split(s, ":")
		if len(p) < 2 || len(p) > 3 {
			return 0, errors.New("视频时长格式无法识别")
		}
		for i, t := range p {
			n, e := strconv.ParseFloat(t, 64)
			if e != nil || n < 0 || (i > 0 && n >= 60) {
				return 0, errors.New("视频时长格式无法识别")
			}
			seconds = seconds*60 + n
		}
	} else {
		n, e := strconv.ParseFloat(s, 64)
		if e != nil {
			return 0, errors.New("视频时长格式无法识别")
		}
		seconds = n
	}
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || seconds > 86400 {
		return 0, errors.New("视频时长需为1天以内的正秒数或时分秒文本；不会猜测毫秒单位")
	}
	return seconds, nil
}
func eligible(course Object, g Global, now time.Time) (bool, string) {
	id := str(course["id"])
	if len(g.CourseIDs) > 0 {
		found := false
		for _, v := range g.CourseIDs {
			if id == v {
				found = true
			}
		}
		if !found {
			return false, "不在course_ids选择范围"
		}
	}
	if !g.IncludeEnded {
		end := str(course["endDate"])
		if end != "" {
			loc := time.FixedZone("Asia/Shanghai", 8*3600)
			var date time.Time
			var e error
			for _, layout := range []string{"2006-01-02", "2006-01-02 15:04:05", time.RFC3339} {
				date, e = time.ParseInLocation(layout, end, loc)
				if e == nil {
					break
				}
			}
			if e != nil {
				return false, "截止日期无法识别"
			}
			if len(end) == 10 {
				date = date.AddDate(0, 0, 1)
			}
			if !now.Before(date) {
				return false, "课程期限已结束（不是已学完）"
			}
		}
	}
	return true, ""
}

// 仅按真实经过的时间推进会话；不倍速、不跳过剩余时长。
// 网页播放器、后台会话是否具有相同服务器认可效果，必须通过现场验证。
type Clock interface {
	Now() time.Time
	Wait(context.Context, time.Duration) error
}
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) Wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func studyNode(ctx context.Context, c *Client, course string, node Object, g Global, clock Clock, onProgress func(float64), onHeartbeat ...func(float64, time.Duration)) (progress float64, err error) {
	return studyNodeWithTimer(ctx, c, course, node, g, clock, onProgress, nil, onHeartbeat...)
}

// 计时回调仅用于展示，与平台进度及最终完成判定分开。
func studyNodeWithTimer(ctx context.Context, c *Client, course string, node Object, g Global, clock Clock, onProgress, onTimer func(float64), onHeartbeat ...func(float64, time.Duration)) (progress float64, err error) {
	id := str(node["id"])
	if id == "" {
		return 0, errors.New("节点缺少ID")
	}
	if integer(node["lock"]) == 1 {
		return 0, errors.New("节点受锁定限制，未尝试启动")
	}
	progress, err = c.progress(ctx, course, id)
	if err != nil {
		return progress, err
	}
	if progress >= 100 {
		return progress, nil
	}
	duration, e := durationSeconds(node["videoDuration"])
	if e != nil {
		return progress, e
	}
	r, e := c.request(ctx, "POST", "/user/study_session_start", nil, Object{"schoolId": c.School, "userId": c.User, "nodeId": id, "courseId": course, "terminal": "web"}, false)
	if e != nil {
		return progress, e
	}
	session := ""
	switch x := r["data"].(type) {
	case string:
		session = x
	default:
		session = field(obj(x), "sessionId", "id")
	}
	// json.Number使用下面的通用分支处理。
	if session == "" && r["data"] != nil {
		if _, ok := r["data"].(map[string]any); !ok {
			session = str(r["data"])
		}
	}
	if session == "" {
		return progress, errors.New("会话启动响应缺少sessionId；未继续提交")
	}
	ended := false
	endSession := func() error {
		if ended {
			return nil
		}
		ended = true
		endCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		_, endErr := c.raw(endCtx, "POST", "/user/study_session_end", nil, Object{"sessionId": session})
		return endErr
	}
	defer func() {
		if e := endSession(); e != nil && err == nil {
			err = fmt.Errorf("结束学习会话失败: %w", e)
		}
	}()
	start := clock.Now()
	base := progress
	remaining := time.Duration(duration * (100 - base) / 100 * float64(time.Second))
	timer := timedProgressDisplay{clock: clock, start: start, base: base, duration: duration, remaining: remaining, last: int(math.Floor(base)) - 1, notify: onTimer}
	poll := time.Duration(duration / 100 * float64(time.Second))
	if poll < time.Second {
		poll = time.Second
	}
	heartbeat := time.Duration(g.Heartbeat) * time.Second
	if poll > heartbeat {
		poll = heartbeat
	}
	// 正常网页的即时首心跳也返回500；等待首个实际计时间隔后再提交。
	nextHeartbeat := heartbeat
	lastDisplayed := -1
	emitConfirmed := func(p float64) {
		whole := int(math.Floor(p))
		if whole > lastDisplayed {
			lastDisplayed = whole
			onProgress(p)
		}
	}
	for {
		if ctx.Err() != nil {
			return progress, ctx.Err()
		}
		elapsed := clock.Now().Sub(start)
		if elapsed < 0 {
			return progress, errors.New("时钟异常")
		}
		value := math.Min(100, base+elapsed.Seconds()/duration*100)
		timer.emit(elapsed)
		if ctx.Err() != nil {
			return progress, ctx.Err()
		}
		sentHeartbeat := false
		if elapsed >= nextHeartbeat || elapsed >= remaining {
			_, e = c.request(ctx, "POST", "/user/study_session_heartbeat", nil, Object{"sessionId": session, "progress": strconv.Itoa(int(math.Floor(value)))}, false)
			if e != nil {
				return progress, e
			}
			nextHeartbeat = elapsed + heartbeat
			sentHeartbeat = true
		}
		observed, readErr := c.progress(ctx, course, id)
		if readErr != nil {
			return progress, readErr
		}
		progress = observed
		if sentHeartbeat && int(math.Floor(progress)) <= lastDisplayed && len(onHeartbeat) > 0 {
			onHeartbeat[0](progress, elapsed)
		}
		emitConfirmed(progress)
		if elapsed >= remaining || progress >= 100 {
			break
		}
		wait := poll
		if nextHeartbeat-elapsed < wait {
			wait = nextHeartbeat - elapsed
		}
		if remaining-elapsed < wait {
			wait = remaining - elapsed
		}
		if e = timer.wait(ctx, wait); e != nil {
			return progress, e
		}
	}
	if e = endSession(); e != nil {
		return progress, fmt.Errorf("结束学习会话失败: %w", e)
	}
	// 最终成功以回读为准，心跳请求成功不等于节点完成。
	for attempt := 0; attempt < 3; attempt++ {
		observed, readErr := c.progress(ctx, course, id)
		if readErr != nil {
			return progress, readErr
		}
		progress = observed
		if progress >= 100 {
			emitConfirmed(progress)
			return progress, nil
		}
		if attempt < 2 {
			if e = clock.Wait(ctx, 2*time.Second); e != nil {
				return progress, e
			}
		}
	}
	return progress, fmt.Errorf("会话时长已走完，但平台回读进度为%.1f%%，未确认完成", progress)
}

func processCourse(ctx context.Context, c *Client, course Object, g Global, study bool, clock Clock, notify func(NodeResult)) CourseResult {
	out := CourseResult{Account: c.Account.Label, ID: str(course["id"]), Name: field(course, "courseName", "name"), Status: "checked"}
	summary, e := c.studyProgress(ctx, out.ID)
	if e != nil {
		out.Status = "failed"
		out.Message = e.Error()
		return out
	}
	if summary.Total > 0 && summary.Completed == summary.Total {
		p := 100.0
		out.Progress = &p
		out.Status = "skipped_complete"
		out.Message = "已学习 100%, 跳过（视频进度）"
		return out
	}
	chapters, e := c.chapters(ctx, out.ID)
	if e != nil {
		out.Status = "failed"
		out.Message = e.Error()
		return out
	}
	sort.SliceStable(chapters, func(i, j int) bool { return integer(chapters[i]["sort"]) < integer(chapters[j]["sort"]) })
	for chapterIndex, ch := range chapters {
		nodes := arr(ch["children"])
		sort.SliceStable(nodes, func(i, j int) bool { return integer(nodes[i]["sort"]) < integer(nodes[j]["sort"]) })
		for lessonIndex, node := range nodes {
			if ctx.Err() != nil {
				out.Status = "cancelled"
				out.Message = "任务已停止"
				return out
			}
			n := NodeResult{ID: str(node["id"]), Name: str(node["name"]), Status: "current", ChapterID: str(ch["id"]), ChapterName: str(ch["name"]), ChapterIndex: chapterIndex + 1, LessonIndex: lessonIndex + 1}
			if integer(node["tabVideo"]) != 1 {
				n.Status = "skipped"
				n.Message = "非视频节点"
				out.Nodes = append(out.Nodes, n)
				continue
			}
			if saved, ok := summary.Nodes[n.ID]; ok && saved >= 100 {
				n.Status = "skipped_complete"
				n.Progress = &saved
				out.Nodes = append(out.Nodes, n)
				continue
			}
			if saved, ok := summary.Nodes[n.ID]; ok {
				n.Progress = &saved
			}
			notify(n)
			n.Status = "checked"
			if integer(node["tabVideo"]) != 1 {
				n.Status = "skipped"
				n.Message = "非视频节点：未处理作业、考试或其他内容"
			} else if study {
				p, e := studyNodeWithTimer(ctx, c, out.ID, node, g, clock, func(p float64) {
					n.Progress = &p
					current := n
					current.Status = "running"
					current.Progress = &p
					current.Message = ""
					notify(current)
				}, func(p float64) {
					n.TimedProgress = &p
					current := n
					current.Status = "running"
					current.Event = "timer"
					current.Message = ""
					notify(current)
				})
				n.Progress = &p
				if e != nil {
					n.Status = "failed"
					n.Message = e.Error()
					out.Status = "paused"
					out.Message = "当前视频未确认完成，已暂停课程；不会跳到下一节: " + e.Error()
					if errors.Is(e, context.Canceled) {
						n.Status = "cancelled"
						n.Message = "收到停止指令，当前会话已结束"
						out.Status = "cancelled"
						out.Message = n.Message
					}
				} else {
					n.Status = "confirmed"
				}
			}
			out.Nodes = append(out.Nodes, n)
			notify(n)
			if errors.Is(ctx.Err(), context.Canceled) {
				out.Status = "cancelled"
				return out
			}
			if out.Status == "paused" {
				return out
			}
			c.mu.Lock()
			paused := c.paused
			c.mu.Unlock()
			if paused {
				out.Status = "paused"
				out.Message = "该账号需恢复认证"
				return out
			}
		}
	}
	if study && out.Status != "failed" {
		count := 0
		for _, n := range out.Nodes {
			if n.Status == "confirmed" {
				count++
			}
		}
		if count > 0 {
			out.Status = "videos_confirmed"
			out.Message = "仅视频进度回读完成；不代表学分、作业或结课达标"
		} else {
			out.Status = "skipped"
			out.Message = "没有可执行视频节点"
		}
	}
	return out
}
