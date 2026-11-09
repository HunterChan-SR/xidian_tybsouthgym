// 配置边界：拒绝未知字段，统一解析路径并验证预约范围。
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Config struct {
	fileSHA256              string
	PoolID                  string
	Accounts                []Account
	TestOnly                bool
	ExactDate               string
	VenueNo                 string
	FieldTypeNo             string
	DateAdd                 int
	BeginTime               string
	EndTime                 string
	PreferredFields         []string
	AllowOtherFields        bool
	FieldPrefix             string
	MaxTotalCents           int
	ReleaseTime             string
	WindowSeconds           int
	PollSeconds             int
	RequestTimeoutSeconds   int
	CookieFile              string
	StateDir                string
	AllowLive               bool
	EveningFlexible         bool
	AllDayFallback          bool
	BookingCaptcha          bool
	CaptchaSolverCommand    []string
	CaptchaSolverPersistent bool
	WeekendAfternoon        bool
}

func (c Config) Validate() error {
	if c.CaptchaSolverPersistent && len(c.CaptchaSolverCommand) == 0 {
		return errors.New("CaptchaSolverPersistent需要支持stdio协议的CaptchaSolverCommand")
	}
	if len(c.CaptchaSolverCommand) > 0 && (!c.BookingCaptcha || strings.TrimSpace(c.CaptchaSolverCommand[0]) == "") {
		return errors.New("CaptchaSolverCommand需要开启BookingCaptcha并指定可执行命令")
	}
	if c.VenueNo != "01" || !((c.FieldTypeNo == "001" && c.FieldPrefix == "YMQ" && !c.TestOnly) || (c.FieldTypeNo == "007" && c.FieldPrefix == "BXQYMQ" && !c.TestOnly) || (c.FieldTypeNo == "002" && c.FieldPrefix == "PPQ" && c.TestOnly)) {
		return errors.New("仅支持已核验的01/001南校区、01/007北校区普通羽毛球及获准乒乓测试")
	}
	if c.AllDayFallback && (!c.EveningFlexible || c.FieldTypeNo != "007") {
		return errors.New("全天备选仅用于北校区普通场晚间择优")
	}
	if c.EveningFlexible && c.FieldTypeNo != "007" {
		return errors.New("晚间择优仅用于北校区普通羽毛球")
	}
	if c.FieldTypeNo == "007" {
		if c.DateAdd != 2 || c.MaxTotalCents > 4000 || c.BeginTime != "19:00" || c.EndTime != "21:00" || !c.EveningFlexible {
			return errors.New("北校区仅允许两天后晚间择优且总价不超过40元")
		}
		for _, field := range c.PreferredFields {
			if !northField(field) {
				return errors.New("北校区仅允许普通场1至4号")
			}
		}
	}
	if c.WeekendAfternoon && (c.FieldTypeNo != "001" || c.BeginTime != "19:00" || c.EndTime != "21:00" || c.MaxTotalCents > 2400) {
		return errors.New("周末下午优先仅用于南校区原晚间目标、24元内")
	}
	b, e := minutes(c.BeginTime)
	if e != nil {
		return e
	}
	end, e := minutes(c.EndTime)
	if e != nil || end <= b {
		return errors.New("结束时间必须在开始时间之后")
	}
	if c.TestOnly && (c.MaxTotalCents > 600 || end-b != 60 || c.ExactDate == "") {
		return errors.New("乒乓球测试必须指定绝对日期、一小时且不超过6元")
	}
	if c.DateAdd < 0 || c.DateAdd > 7 {
		return errors.New("DateAdd 必须在 0–7 天内")
	}
	if c.MaxTotalCents <= 0 || c.MaxTotalCents > 100000 {
		return errors.New("必须设置有效的总价上限（分）")
	}
	if _, e = time.Parse("15:04:05", c.ReleaseTime); e != nil {
		return errors.New("放票时间需为 HH:MM:SS")
	}
	if c.WindowSeconds < 1 || c.WindowSeconds > 300 || c.PollSeconds < 2 || c.PollSeconds > 60 || c.RequestTimeoutSeconds < 1 || c.RequestTimeoutSeconds > 30 {
		return errors.New("窗口/轮询/超时设置无效")
	}
	if (c.CookieFile == "" && len(c.Accounts) == 0) || c.StateDir == "" {
		return errors.New("缺少会话或状态路径")
	}
	if !c.AllowOtherFields && len(c.PreferredFields) == 0 {
		return errors.New("未选择场地")
	}
	for _, f := range c.PreferredFields {
		if !strings.HasPrefix(f, c.FieldPrefix) {
			return errors.New("场地类型不匹配")
		}
	}
	for _, ch := range c.PoolID {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
			return errors.New("PoolID含非法字符")
		}
	}
	if len(c.Accounts) > 1 && c.PoolID == "" {
		return errors.New("多账号必须设置共用 PoolID，防止重复占场")
	}
	names := map[string]bool{}
	for _, a := range c.Accounts {
		if a.Name == "" || a.CookieFile == "" || names[a.Name] {
			return errors.New("账号别名和会话路径不能为空，别名不能重复")
		}
		names[a.Name] = true
	}
	if len(c.Accounts) > 10 {
		return errors.New("账号池最多10个已授权账号")
	}
	return nil
}

func loadConfig(path string) (Config, error) {
	var c Config
	b, e := os.ReadFile(path)
	if e != nil {
		return c, e
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if e = d.Decode(&c); e != nil {
		return c, e
	}
	if e = d.Decode(new(any)); e != io.EOF {
		return c, errors.New("配置包含多余内容")
	}
	sourceHash := sha256.Sum256(b)
	c.fileSHA256 = hex.EncodeToString(sourceHash[:])
	dir := filepath.Dir(path)
	for i := range c.Accounts {
		if !filepath.IsAbs(c.Accounts[i].CookieFile) {
			c.Accounts[i].CookieFile = filepath.Join(dir, c.Accounts[i].CookieFile)
		}
	}
	if !filepath.IsAbs(c.CookieFile) {
		c.CookieFile = filepath.Join(dir, c.CookieFile)
	}
	if !filepath.IsAbs(c.StateDir) {
		c.StateDir = filepath.Join(dir, c.StateDir)
	}
	return c, c.Validate()
}
