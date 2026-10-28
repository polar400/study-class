//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

type Session struct {
	Token   string         `json:"token"`
	Cookies []*http.Cookie `json:"cookies"`
}
type dataBlob struct {
	Size uint32
	Data *byte
}

func blob(b []byte) dataBlob {
	if len(b) == 0 {
		return dataBlob{}
	}
	return dataBlob{uint32(len(b)), &b[0]}
}
func protect(b []byte, decrypt bool) ([]byte, error) {
	proc := "CryptProtectData"
	if decrypt {
		proc = "CryptUnprotectData"
	}
	in := blob(b)
	var out dataBlob
	// CRYPTPROTECT_UI_FORBIDDEN；DPAPI默认绑定当前Windows用户。
	ok, _, _ := syscall.NewLazyDLL("crypt32.dll").NewProc(proc).Call(uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0, 1, uintptr(unsafe.Pointer(&out)))
	if ok == 0 {
		return nil, errors.New("Windows会话加解密失败")
	}
	defer syscall.NewLazyDLL("kernel32.dll").NewProc("LocalFree").Call(uintptr(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}
func sessionPath(dir string, a Account) string {
	s := sha256.Sum256([]byte(a.BaseURL + "\x00" + a.Username))
	return filepath.Join(dir, hex.EncodeToString(s[:16])+".dpapi")
}
func readSession(dir string, a Account) (Session, error) {
	var s Session
	if dir == "" {
		return s, os.ErrNotExist
	}
	b, e := os.ReadFile(sessionPath(dir, a))
	if e != nil {
		return s, e
	}
	b, e = protect(b, true)
	if e != nil {
		return s, e
	}
	e = json.Unmarshal(b, &s)
	return s, e
}
func writeSession(dir string, a Account, s Session) error {
	if dir == "" {
		return nil
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		return errors.New("无法建立会话目录")
	}
	b, e := json.Marshal(s)
	if e != nil {
		return e
	}
	b, e = protect(b, false)
	if e != nil {
		return e
	}
	p := sessionPath(dir, a)
	if e = os.WriteFile(p+".tmp", b, 0600); e != nil {
		return errors.New("无法保存加密会话")
	}
	if e = os.Rename(p+".tmp", p); e != nil {
		return errors.New("无法更新加密会话")
	}
	return nil
}
