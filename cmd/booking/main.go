// 命令行入口：解析参数、加载会话并保存本轮结果。
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"
)

var buildID = "development"

func main() {
	configPath := flag.String("config", "booking.json", "配置路径")
	mode := flag.String("mode", "probe", "probe: 只读检查; watch: 放票窗口只读观察; book: 真实预约; captcha-preview: 人工滑块预演，不提交订单")
	flag.Parse()
	if *mode != "probe" && *mode != "watch" && *mode != "book" && *mode != "test-book" && *mode != "cancel-test" && *mode != "captcha-preview" {
		fmt.Fprintln(os.Stderr, "无效 mode")
		os.Exit(2)
	}
	cfg, e := loadConfig(*configPath)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(2)
	}
	unlock, e := acquire(cfg.StateDir)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(2)
	}
	accounts := cfg.Accounts
	if len(accounts) == 0 {
		accounts = []Account{{Name: "primary", CookieFile: cfg.CookieFile}}
	}
	var clients []*Client
	eventPath := filepath.Join(cfg.StateDir, "events", time.Now().In(shanghai).Format("2006-01-02")+".jsonl")
	for _, account := range accounts {
		session, err := loadSession(account.CookieFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "账号会话不可用:", account.Name)
			continue
		}
		client := newClient(session, time.Duration(cfg.RequestTimeoutSeconds)*time.Second)
		client.Name = account.Name
		client.EventLog = eventPath
		clients = append(clients, client)
	}
	if len(clients) > 0 {
		configBytes, _ := json.Marshal(cfg)
		sum := sha256.Sum256(configBytes)
		clients[0].event(map[string]any{"event": "run_start", "at": time.Now().In(shanghai).Format(time.RFC3339Nano), "buildID": buildID, "configSHA256": hex.EncodeToString(sum[:]), "configFileSHA256": cfg.fileSHA256, "mode": *mode, "accountCount": len(clients)})
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	var out Outcome
	if *mode == "captcha-preview" {
		out, e = previewCaptcha(ctx, clients, cfg)
	} else {
		out, e = runPool(ctx, clients, cfg, *mode)
	}
	if e != nil {
		out.Status = faultKind(e)
		if out.Status == "local_error" {
			out.Detail = e.Error()
		}
	}
	out.At = time.Now().In(shanghai).Format(time.RFC3339)
	if len(clients) > 0 {
		clients[0].event(map[string]any{"event": "run_end", "at": out.At, "mode": *mode, "status": out.Status, "account": out.Account, "date": out.TargetDate, "field": out.FieldNo, "totalCents": out.TotalCents, "observationOnlyReason": out.ObservationOnlyReason})
	}
	latest := "latest.json"
	if *mode == "captcha-preview" {
		latest = "latest-captcha-preview.json"
	}
	writeErr := saveJSON(filepath.Join(cfg.StateDir, latest), out)
	historyErr := saveJSON(filepath.Join(cfg.StateDir, "runs", time.Now().In(shanghai).Format("20060102-150405")+"-"+*mode+".json"), out)
	if writeErr == nil {
		writeErr = historyErr
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
	unlock()
	if e != nil || writeErr != nil {
		os.Exit(1)
	}
}
