package main

import (
	_ "embed"
	"fmt"
	"io"
	"log"
	"strings"
	"time"
)

//go:embed polar-logo.txt
var polarLogo string

var polarBanner = func() string {
	lines := append([]string{""}, strings.Split(strings.TrimRight(strings.ReplaceAll(polarLogo, "\r\n", "\n"), "\n"), "\n")...)
	lines = append(lines, "", fmt.Sprintf("study class  |  v%s  |  Windows amd64", version))
	lines = append(lines, "GitHub: https://github.com/polar400/study-class")
	width := 66
	for _, line := range lines {
		if len(line) > width {
			width = len(line)
		}
	}
	var b strings.Builder
	border := "+" + strings.Repeat("-", width+2) + "+"
	b.WriteString("\n" + border + "\n")
	for _, line := range lines {
		fmt.Fprintf(&b, "| %-*s |\n", width, line)
	}
	b.WriteString(border + "\n\n")
	return b.String()
}()

type consoleWriter struct {
	dst   io.Writer
	color bool
}

// 状态只由调用点传递，不根据课程名、错误文案或百分比猜测完成。
func consoleMessage(status, format string, args ...any) string {
	return "\x00" + status + "\x00" + fmt.Sprintf(format, args...)
}

func logStatusf(status, format string, args ...any) {
	log.Print(consoleMessage(status, format, args...))
}

func (w consoleWriter) Write(p []byte) (int, error) {
	message := strings.TrimSuffix(string(p), "\n")
	status := ""
	if strings.HasPrefix(message, "\x00") {
		if end := strings.IndexByte(message[1:], 0); end >= 0 {
			status, message = message[1:1+end], message[2+end:]
		}
	}
	label, color := "INFO", "\x1b[94m"
	switch status {
	case "failed", "paused", "needs_attention":
		label, color = "ERROR", "\x1b[91m"
	case "confirmed", "skipped_complete", "videos_confirmed":
		color = "\x1b[92m"
	}
	if w.color {
		label = color + label + "\x1b[0m"
	}
	_, e := fmt.Fprintf(w.dst, "%s[%s]  %s\n", label, time.Now().Format("2006-01-02 15:04:05"), message)
	if e != nil {
		return 0, e
	}
	return len(p), nil
}

func nodeLogLines(account, course string, n NodeResult, newChapter bool) []string {
	prefix := fmt.Sprintf("[%s] [%s]", cleanText(account), cleanText(course))
	if n.Event == "heartbeat" {
		return nil
	}
	if n.Event == "timer" {
		if n.TimedProgress == nil {
			return nil
		}
		line := fmt.Sprintf("%s  %s[nodeId=%s]  计时进度: %d%%", prefix, cleanText(n.Name), n.ID, int(*n.TimedProgress))
		if *n.TimedProgress >= 100 {
			line += ", 等待平台确认"
		}
		return []string{line}
	}
	if n.Status == "current" {
		lines := []string{}
		if newChapter {
			lines = append(lines, fmt.Sprintf("%s  第 %d 章  %s", prefix, n.ChapterIndex, cleanText(n.ChapterName)))
		}
		lines = append(lines, fmt.Sprintf("%s  当前第 %d 课, [%s][nodeId=%s]", prefix, n.LessonIndex, cleanText(n.Name), n.ID))
		return lines
	}
	state := n.Status
	switch state {
	case "running":
		state = ""
	case "confirmed":
		state = "平台确认完成"
	case "failed":
		state = "暂停该课程"
	case "cancelled":
		state = "已停止"
	case "checked":
		state = "只读检查"
	case "skipped":
		state = "跳过"
	}
	line := fmt.Sprintf("%s  %s[nodeId=%s]", prefix, cleanText(n.Name), n.ID)
	if state != "" {
		line += "  " + state
	}
	if n.Progress != nil {
		line += fmt.Sprintf(", 当前进度: %d%%", int(*n.Progress))
	}
	if n.Message != "" {
		line += ", 原因: " + cleanText(n.Message)
	}
	return []string{line}
}

func printConsolePreview(w io.Writer, color bool) {
	fmt.Fprint(w, polarBanner)
	out := consoleWriter{dst: w, color: color}
	fmt.Fprintln(out, "排版预览：以下均为示例数据，未连接平台、未启动课程。")
	n := NodeResult{ID: "1539735", Name: "5.5.1 智能生产线", Status: "current", ChapterIndex: 5, ChapterName: "智能制造设备", LessonIndex: 5}
	for _, line := range nodeLogLines("B00000001", "智能制造与数字化工厂", n, true) {
		fmt.Fprintln(out, line)
	}
	for _, p := range []float64{20, 21, 22, 23} {
		n.Status = "running"
		n.Event = "timer"
		n.TimedProgress = &p
		for _, line := range nodeLogLines("B00000001", "智能制造与数字化工厂", n, false) {
			fmt.Fprintln(out, line)
		}
	}
	p := float64(100)
	n.TimedProgress = &p
	for _, line := range nodeLogLines("示例账号", "示例课程", n, false) {
		fmt.Fprintln(out, consoleMessage(n.Status, "%s", line))
	}
	n.Event = ""
	n.Status = "confirmed"
	n.Progress = &p
	for _, line := range nodeLogLines("示例账号", "示例课程", n, false) {
		fmt.Fprintln(out, consoleMessage(n.Status, "%s", line))
	}
	fmt.Fprintln(out, consoleMessage("skipped_complete", "[示例账号] 当前课程[示例课程] 进度: 已学习 100%%, 跳过"))
	n.Status = "failed"
	n.Message = "示例：当前视频未确认完成，停留本节，未启动下一节"
	for _, line := range nodeLogLines("B00000001", "智能制造与数字化工厂", n, false) {
		fmt.Fprintln(out, consoleMessage(n.Status, "%s", line))
	}
}
