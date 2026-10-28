//go:build windows

package main

import (
	"os"
	"syscall"
)

// 重定向输出保持纯文本；真实 Windows 控制台启用 ANSI，并在退出时恢复模式。
func enableConsoleColor(f *os.File) (bool, func()) {
	handle := syscall.Handle(f.Fd())
	var mode uint32
	if syscall.GetConsoleMode(handle, &mode) != nil {
		return false, func() {}
	}
	setMode := syscall.NewLazyDLL("kernel32.dll").NewProc("SetConsoleMode")
	ok, _, _ := setMode.Call(uintptr(handle), uintptr(mode|0x0004))
	if ok == 0 {
		return false, func() {}
	}
	return true, func() { setMode.Call(uintptr(handle), uintptr(mode)) }
}
