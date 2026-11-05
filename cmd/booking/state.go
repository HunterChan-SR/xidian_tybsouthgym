// 本地原子状态写入与互斥运行锁。
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func saveJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	tmp, e := os.CreateTemp(filepath.Dir(path), ".state-*")
	if e != nil {
		return e
	}
	name := tmp.Name()
	defer os.Remove(name)
	if e = tmp.Chmod(0600); e == nil {
		_, e = tmp.Write(b)
	}
	if e == nil {
		e = tmp.Sync()
	}
	closeErr := tmp.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, path)
}

func acquire(dir string) (func(), error) {
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	path := filepath.Join(dir, "runner.lock")
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return nil, errors.New("已有运行实例或上次异常遗留锁；请先检查进程和订单")
	}
	_, e = fmt.Fprintf(f, "pid=%d\nstarted=%s\n", os.Getpid(), time.Now().In(shanghai).Format(time.RFC3339))
	ce := f.Close()
	if e != nil || ce != nil {
		os.Remove(path)
		return nil, errors.New("无法写入运行锁")
	}
	return func() { os.Remove(path) }, nil
}
