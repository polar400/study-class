package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestTimedProgressVisitsEveryIntegerAndResumes(t *testing.T) {
	for _, base := range []float64{0, 30.5} {
		t.Run(fmt.Sprint(base), func(t *testing.T) {
			clock := &fakeClock{now: time.Unix(0, 0)}
			startedAt := clock.Now()
			ended := false
			c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/user/get_study_progress":
					p := base
					if ended {
						p = 100
					}
					progressReply(w, p)
				case "/api/user/study_session_start":
					reply(w, "timer-test-session")
				case "/api/user/study_session_end":
					ended = true
					reply(w, nil)
				default:
					reply(w, nil)
				}
			})
			var estimated, observed []float64
			var reached100 time.Duration
			p, err := studyNodeWithTimer(context.Background(), c, "1", Object{"id": 10, "videoDuration": 60}, Global{Heartbeat: 30}, clock,
				func(p float64) { observed = append(observed, p) },
				func(p float64) {
					estimated = append(estimated, p)
					if p == 100 {
						reached100 = clock.Now().Sub(startedAt)
					}
				})
			if err != nil || p != 100 {
				t.Fatalf("study: progress=%v err=%v", p, err)
			}
			first := int(math.Floor(base))
			if len(estimated) != 101-first {
				t.Fatalf("integer progress count=%d want=%d values=%v", len(estimated), 101-first, estimated)
			}
			for i, value := range estimated {
				if value != float64(first+i) {
					t.Fatalf("estimate[%d]=%v want=%d", i, value, first+i)
				}
			}
			remaining := time.Duration(60 * (100 - base) / 100 * float64(time.Second))
			if reached100 < remaining {
				t.Fatalf("timer reached 100 too early: elapsed=%v remaining=%v", reached100, remaining)
			}
			if !reflect.DeepEqual(observed, []float64{base, 100}) {
				t.Fatalf("timer polluted platform progress: %v", observed)
			}
		})
	}
}

func TestTimedProgressDoesNotChangePlatformRequests(t *testing.T) {
	type requestEvent struct {
		Path     string
		Elapsed  time.Duration
		Progress string
	}
	run := func(timer bool) ([]requestEvent, time.Duration) {
		t.Helper()
		clock := &fakeClock{now: time.Unix(0, 0)}
		startedAt := clock.Now()
		ended := false
		var events []requestEvent
		c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			event := requestEvent{Path: r.URL.Path, Elapsed: clock.Now().Sub(startedAt)}
			if strings.HasSuffix(r.URL.Path, "study_session_heartbeat") {
				var body Object
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				event.Progress = str(body["progress"])
			}
			events = append(events, event)
			switch r.URL.Path {
			case "/api/user/get_study_progress":
				p := 30.0
				if ended {
					p = 100
				}
				progressReply(w, p)
			case "/api/user/study_session_start":
				reply(w, "timer-test-session")
			case "/api/user/study_session_end":
				ended = true
				reply(w, nil)
			default:
				reply(w, nil)
			}
		})
		var p float64
		var err error
		node := Object{"id": 10, "videoDuration": 1000}
		if timer {
			p, err = studyNodeWithTimer(context.Background(), c, "1", node, Global{Heartbeat: 30}, clock, func(float64) {}, func(float64) {})
		} else {
			p, err = studyNode(context.Background(), c, "1", node, Global{Heartbeat: 30}, clock, func(float64) {})
		}
		if err != nil || p != 100 {
			t.Fatalf("study timer=%v: progress=%v err=%v", timer, p, err)
		}
		return events, clock.Now().Sub(startedAt)
	}
	baseline, baselineElapsed := run(false)
	withTimer, timerElapsed := run(true)
	if !reflect.DeepEqual(baseline, withTimer) {
		t.Fatalf("timer changed platform request sequence/timing:\nwithout=%v\nwith=%v", baseline, withTimer)
	}
	if baselineElapsed != 700*time.Second || timerElapsed != baselineElapsed {
		t.Fatalf("timer changed session duration: baseline=%v timer=%v", baselineElapsed, timerElapsed)
	}
}

func TestTimedProgress100StillPausesUnconfirmedCourse(t *testing.T) {
	var starts []string
	var notifications []NodeResult
	c, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/get_study_progress":
			reply(w, Object{"summary": Object{"totalNodes": 2, "completedNodes": 0}, "nodeProgressList": []Object{{"nodeId": 10, "progressPercent": 0}, {"nodeId": 11, "progressPercent": 0}}})
		case "/api/user/yee_node_select":
			reply(w, []Object{{"id": 1, "children": []Object{{"id": 10, "name": "first", "tabVideo": 1, "videoDuration": 2}, {"id": 11, "name": "next", "tabVideo": 1, "videoDuration": 2}}}})
		case "/api/user/study_session_start":
			var body Object
			_ = json.NewDecoder(r.Body).Decode(&body)
			starts = append(starts, str(body["nodeId"]))
			reply(w, "timer-test-session")
		default:
			reply(w, nil)
		}
	})
	result := processCourse(context.Background(), c, Object{"id": 1}, Global{Heartbeat: 30}, true, &fakeClock{}, func(n NodeResult) {
		notifications = append(notifications, n)
	})
	if result.Status != "paused" || strings.Join(starts, ",") != "10" {
		t.Fatalf("unconfirmed course advanced: status=%s starts=%v", result.Status, starts)
	}
	sawWaiting := false
	for _, n := range notifications {
		if n.Status == "confirmed" {
			t.Fatal("local timer must never confirm a video")
		}
		if n.Event == "timer" && n.TimedProgress != nil && *n.TimedProgress == 100 {
			sawWaiting = true
			if n.Status != "running" || n.Progress == nil || *n.Progress != 0 {
				t.Fatalf("timer completion polluted platform status: %+v", n)
			}
		}
	}
	if !sawWaiting || len(result.Nodes) != 1 || result.Nodes[0].Status != "failed" || result.Nodes[0].Progress == nil || *result.Nodes[0].Progress != 0 {
		t.Fatalf("missing waiting/failure state: sawWaiting=%v result=%+v", sawWaiting, result)
	}
}

func TestTimedProgressLogColorsAndJSONStayDistinct(t *testing.T) {
	platform, timed := 0.0, 100.0
	n := NodeResult{ID: "10", Name: "video", Status: "running", Event: "timer", Progress: &platform, TimedProgress: &timed}
	lines := nodeLogLines("account", "course", n, false)
	if len(lines) != 1 || !strings.Contains(lines[0], "计时") || !strings.Contains(lines[0], "100%") || !strings.Contains(lines[0], "等待平台确认") || strings.Contains(lines[0], "平台确认完成") {
		t.Fatalf("timer100 must be labelled as waiting: %v", lines)
	}
	var out bytes.Buffer
	w := consoleWriter{dst: &out, color: true}
	fmt.Fprintln(w, consoleMessage(n.Status, "%s", lines[0]))
	if !strings.HasPrefix(out.String(), "\x1b[94mINFO\x1b[0m[") {
		t.Fatalf("timer100 must remain blue: %q", out.String())
	}
	data, err := json.Marshal(n)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["progress"] != float64(0) || fields["timed_progress"] != float64(100) || fields["status"] != "running" {
		t.Fatalf("serialized platform/timer fields mixed: %s", data)
	}
	for _, tc := range []struct{ status, prefix string }{
		{"confirmed", "\x1b[92mINFO\x1b[0m["},
		{"failed", "\x1b[91mERROR\x1b[0m["},
	} {
		n.Status, n.Event = tc.status, ""
		out.Reset()
		for _, line := range nodeLogLines("account", "course", n, false) {
			fmt.Fprintln(w, consoleMessage(n.Status, "%s", line))
		}
		if !strings.HasPrefix(out.String(), tc.prefix) {
			t.Fatalf("status %s wrong color: %q", tc.status, out.String())
		}
	}
}
