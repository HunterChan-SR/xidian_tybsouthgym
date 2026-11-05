// 领域数据与纯解析函数：不执行网络请求或写入状态。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

type Session struct {
	Cookie    string
	UserAgent string
}

type Slot struct {
	DateBeginTime string
	DateEndTime   string
	FieldNo       string
	FieldName     string
	FieldTypeNo   string
	BeginTime     string
	EndTime       string
	FieldState    json.RawMessage
	TimeStatus    json.RawMessage
	FinalPrice    json.RawMessage
}

type Item struct {
	FieldNo     string
	FieldTypeNo string
	FieldName   string
	BeginTime   string
	Endtime     string
	Price       string
}

type Candidate struct {
	Items      []Item
	TotalCents int
}

type Outcome struct {
	ObservationOnlyReason string

	BeginTime   string
	EndTime     string
	Account     string
	IsTest      bool
	FieldTypeNo string
	Status      string
	At          string
	TargetDate  string
	FieldNo     string
	TotalCents  int
	OrderID     string
	Detail      string
}

type Fault struct {
	Kind       string
	Reason     string
	RetryAfter time.Duration
}

func (f *Fault) Error() string { return f.Kind }

type Envelope struct {
	Type       json.RawMessage
	Message    string
	ResultData json.RawMessage
}

func atom(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return strings.TrimSpace(string(raw))
}

func minutes(s string) (int, error) {
	if len(s) != 5 || s[2] != ':' {
		return 0, errors.New("时间需为 HH:MM")
	}
	h, e := strconv.Atoi(s[:2])
	if e != nil {
		return 0, e
	}
	m, e := strconv.Atoi(s[3:])
	if e != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, errors.New("时间越界")
	}
	return h*60 + m, nil
}

func money(s string) (int, error) {
	parts := strings.Split(s, ".")
	if len(parts) > 2 || len(parts[0]) == 0 {
		return 0, errors.New("价格格式变化")
	}
	for _, c := range parts[0] {
		if c < '0' || c > '9' {
			return 0, errors.New("价格无效")
		}
	}
	n, e := strconv.Atoi(parts[0])
	if e != nil || n > 100000 {
		return 0, errors.New("价格无效")
	}
	frac := "00"
	if len(parts) == 2 {
		if len(parts[1]) < 1 || len(parts[1]) > 2 {
			return 0, errors.New("价格精度无效")
		}
		frac = parts[1]
		if len(frac) == 1 {
			frac += "0"
		}
	}
	for _, c := range frac {
		if c < '0' || c > '9' {
			return 0, errors.New("价格无效")
		}
	}
	m, e := strconv.Atoi(frac)
	return n*100 + m, e
}

func normalizeTime(value string) (string, error) {
	if len(value) == 4 && value[1] == ':' {
		value = "0" + value
	}
	if _, e := minutes(value); e != nil {
		return "", e
	}
	return value, nil
}

func faultKind(e error) string {
	var f *Fault
	if errors.As(e, &f) {
		return f.Kind
	}
	if errors.Is(e, context.Canceled) {
		return "cancelled"
	}
	if errors.Is(e, context.DeadlineExceeded) {
		return "window_closed"
	}
	return "local_error"
}
