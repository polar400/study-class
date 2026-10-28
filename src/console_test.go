package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

func TestConsoleStatusColors(t *testing.T) {
	for _, tc := range []struct{ status, label, color string }{
		{"", "INFO", "94"}, {"running", "INFO", "94"}, {"current", "INFO", "94"},
		{"checked", "INFO", "94"}, {"cancelled", "INFO", "94"}, {"finished", "INFO", "94"},
		{"confirmed", "INFO", "92"}, {"skipped_complete", "INFO", "92"}, {"videos_confirmed", "INFO", "92"},
		{"failed", "ERROR", "91"}, {"paused", "ERROR", "91"}, {"needs_attention", "ERROR", "91"},
	} {
		t.Run(tc.status, func(t *testing.T) {
			var out bytes.Buffer
			w := consoleWriter{dst: &out, color: true}
			// 相同正文包含100%和错误关键词，仍必须以真实状态决定颜色。
			body := "[示例课程100%] 当前进度: 100%, 原因: 最终确认失败"
			input := consoleMessage(tc.status, "%s", body) + "\n"
			if n, err := w.Write([]byte(input)); err != nil || n != len(input) {
				t.Fatalf("write: n=%d err=%v", n, err)
			}
			prefix := "\x1b[" + tc.color + "m" + tc.label + "\x1b[0m["
			if !strings.HasPrefix(out.String(), prefix) || !strings.HasSuffix(out.String(), "]  "+body+"\n") {
				t.Fatalf("unexpected output: %q", out.String())
			}
			if strings.Count(out.String(), "\x1b[") != 2 || strings.ContainsRune(out.String(), 0) {
				t.Fatal("only the label should be colored; internal status must be removed")
			}
		})
	}
}

func TestConsoleAndFileOutputSeparated(t *testing.T) {
	var terminal, file bytes.Buffer
	w := io.MultiWriter(consoleWriter{dst: &terminal, color: true}, consoleWriter{dst: &file})
	for _, status := range []string{"running", "confirmed", "failed"} {
		if _, err := fmt.Fprintln(w, consoleMessage(status, "示例 %s", status)); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(terminal.String(), "\x1b[94mINFO\x1b[0m[") ||
		!strings.Contains(terminal.String(), "\x1b[92mINFO\x1b[0m[") ||
		!strings.Contains(terminal.String(), "\x1b[91mERROR\x1b[0m[") {
		t.Fatal("terminal missing a status color")
	}
	if strings.ContainsAny(file.String(), "\x1b\x00") || strings.Count(file.String(), "INFO[") != 2 || strings.Count(file.String(), "ERROR[") != 1 {
		t.Fatalf("file must remain readable plain text: %q", file.String())
	}
}

func TestConsoleRedirectDisablesColor(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "redirect-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	color, restore := enableConsoleColor(f)
	defer restore()
	if color {
		t.Fatal("redirected file must not enable terminal colors")
	}
}

func TestConsolePreviewShowsAllStates(t *testing.T) {
	var out bytes.Buffer
	printConsolePreview(&out, true)
	for _, text := range []string{"未连接平台、未启动课程", "\x1b[94mINFO\x1b[0m[", "\x1b[92mINFO\x1b[0m[", "\x1b[91mERROR\x1b[0m[", "平台确认完成", "已学习 100%, 跳过"} {
		if !strings.Contains(out.String(), text) {
			t.Fatalf("preview missing %q", text)
		}
	}
}
