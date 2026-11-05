// 订单生命周期：先持久化保护，再提交，最后核验服务器订单。
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func receiptPath(cfg Config, s Session, date string) string {
	if cfg.PoolID != "" {
		return filepath.Join(cfg.StateDir, "orders", cfg.PoolID+"-"+date+".json")
	}
	identity := ""
	for _, name := range []string{"UserId", "WXOpenId"} {
		for _, part := range strings.Split(s.Cookie, ";") {
			kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
			if len(kv) == 2 && kv[0] == name && kv[1] != "" {
				identity = name + "=" + kv[1]
				break
			}
		}
		if identity != "" {
			break
		}
	}
	if identity == "" {
		identity = "local-account"
	}
	sum := sha256.Sum256([]byte(identity))
	return filepath.Join(cfg.StateDir, "orders", hex.EncodeToString(sum[:8])+"-"+date+".json")
}

func guardedSubmit(ctx context.Context, c *Client, cfg Config, cand Candidate, date string) (Outcome, error) {
	out := Outcome{BeginTime: cand.Items[0].BeginTime, EndTime: cand.Items[len(cand.Items)-1].Endtime, Account: c.Name, IsTest: cfg.TestOnly, FieldTypeNo: cfg.FieldTypeNo, Status: "pending", At: time.Now().In(shanghai).Format(time.RFC3339), TargetDate: date, FieldNo: cand.Items[0].FieldNo, TotalCents: cand.TotalCents}
	path := receiptPath(cfg, c.Session, date)
	if _, e := os.Stat(path); e == nil {
		return out, &Fault{Kind: "existing_order_or_uncertain_attempt"}
	} else if !os.IsNotExist(e) {
		return out, e
	}
	proof := url.Values{}
	if cfg.BookingCaptcha {
		var err error
		proof, err = c.bookingCaptcha(ctx, cfg)
		if err != nil {
			return out, err
		}
	}
	if cfg.TestOnly {
		marker := filepath.Join(cfg.StateDir, "test-attempt-"+cfg.ExactDate+".json")
		f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return out, &Fault{Kind: "test_already_attempted"}
		}
		_, err = f.WriteString("{\"attempted\":true}")
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			return out, errors.New("无法持久化测试次数")
		}
	}
	// Persist before request: a crash or timeout must never trigger blind resubmission.
	if e := saveJSON(path, out); e != nil {
		return out, e
	}
	c.event(map[string]any{"at": out.At, "event": "submit", "account": c.Name, "date": date, "field": out.FieldNo, "totalCents": out.TotalCents, "items": cand.Items})
	items, _ := json.Marshal(cand.Items)
	ep := "/Field/OrderField"
	if cand.TotalCents == 0 {
		ep = "/Field/OrderFieldFree"
	}
	proof.Set("checkdata", string(items))
	proof.Set("dateadd", strconv.Itoa(cfg.DateAdd))
	proof.Set("VenueNo", cfg.VenueNo)
	b, e := c.get(ctx, ep, proof)
	if e != nil {
		out.Status = "uncertain"
		out.Detail = "请在平台我的预约核对，程序不会自动重试"
		saveErr := saveJSON(path, out)
		if saveErr != nil {
			return out, saveErr
		}
		return out, &Fault{Kind: "order_uncertain"}
	}
	var env Envelope
	if json.Unmarshal(b, &env) != nil || (atom(env.Type) != "1" && (env.Message == "" || (atom(env.Type) != "0" && atom(env.Type) != "2" && atom(env.Type) != "3"))) {
		out.Status = "uncertain"
		if e = saveJSON(path, out); e != nil {
			return out, e
		}
		return out, &Fault{Kind: "order_uncertain"}
	}
	if atom(env.Type) == "1" {
		out.Status = "reserved_payment_manual"
		out.OrderID = atom(env.ResultData)
		if cand.TotalCents == 0 {
			out.Status = "reserved_free"
		}
		if e = saveJSON(path, out); e != nil {
			return out, e
		}
		return out, nil
	}
	out.Status = "rejected"
	out.Detail = rejectionKind(env.Message)
	if e = saveJSON(filepath.Join(cfg.StateDir, "last-rejection.json"), out); e != nil {
		return out, e
	}
	if e = os.Remove(path); e != nil {
		return out, e
	}
	return out, &Fault{Kind: out.Detail}
}

func (c *Client) verifyOrder(ctx context.Context, cfg Config, out Outcome) error {
	if out.OrderID == "" {
		return &Fault{Kind: "order_verification_required"}
	}
	b, e := c.get(ctx, "/Field/GetPayInfo", url.Values{"OID": {out.OrderID}, "VenueNo": {cfg.VenueNo}})
	if e != nil {
		return e
	}
	var rows []struct {
		FieldNo, OrderDate, BeginTime, EndTime string
		Price                                  json.RawMessage
	}
	if json.Unmarshal(b, &rows) != nil || len(rows) == 0 {
		return &Fault{Kind: "order_verification_required"}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].BeginTime < rows[j].BeginTime })
	current := cfg.BeginTime
	total := 0
	for _, row := range rows {
		date := strings.ReplaceAll(row.OrderDate, "/", "-")
		if len(date) > 10 {
			date = date[:10]
		}
		begin, err := normalizeTime(row.BeginTime)
		if err != nil {
			return &Fault{Kind: "order_verification_required"}
		}
		end, err := normalizeTime(row.EndTime)
		if err != nil {
			return &Fault{Kind: "order_verification_required"}
		}
		if row.FieldNo != out.FieldNo || date != out.TargetDate || begin != current {
			return &Fault{Kind: "order_details_mismatch"}
		}
		price, err := money(atom(row.Price))
		if err != nil {
			return err
		}
		total += price
		current = end
	}
	if current != cfg.EndTime || total != out.TotalCents || total > cfg.MaxTotalCents {
		return &Fault{Kind: "order_details_mismatch"}
	}
	c.event(map[string]any{"at": time.Now().In(shanghai).Format(time.RFC3339Nano), "event": "order_verified", "date": out.TargetDate, "field": out.FieldNo, "totalCents": total})
	return nil
}

func (c *Client) payStatus(ctx context.Context, oid string) (string, error) {
	b, e := c.get(ctx, "/Field/CheckStatus", url.Values{"OID": {oid}})
	if e != nil {
		return "", e
	}
	var rows []struct{ PayStatus json.RawMessage }
	if json.Unmarshal(b, &rows) != nil || len(rows) != 1 {
		return "", &Fault{Kind: "order_verification_required"}
	}
	return atom(rows[0].PayStatus), nil
}

func cancelTest(ctx context.Context, c *Client, cfg Config, out Outcome) (Outcome, error) {
	if !cfg.TestOnly || !out.IsTest || cfg.FieldTypeNo != "002" || out.FieldTypeNo != "002" || out.TargetDate != cfg.ExactDate || out.OrderID == "" {
		return out, &Fault{Kind: "test_cancellation_not_authorized"}
	}
	if e := c.verifyOrder(ctx, cfg, out); e != nil {
		return out, e
	}
	status, e := c.payStatus(ctx, out.OrderID)
	if e != nil {
		return out, e
	}
	if status == "4" {
		out.Status = "test_cancelled"
		return out, saveJSON(receiptPath(cfg, c.Session, out.TargetDate), out)
	}
	if status != "0" {
		return out, &Fault{Kind: "test_order_not_unpaid"}
	}
	out.Status = "test_cancel_pending"
	if e = saveJSON(receiptPath(cfg, c.Session, out.TargetDate), out); e != nil {
		return out, e
	}
	b, e := c.get(ctx, "/Field/FieldCancel", url.Values{"OID": {out.OrderID}})
	// Always verify the server's final state, even if the cancellation response is lost.
	var envelope Envelope
	responseOK := e == nil && json.Unmarshal(b, &envelope) == nil && atom(envelope.Type) == "1"
	status, checkErr := c.payStatus(ctx, out.OrderID)
	if checkErr != nil || status != "4" {
		out.Status = "test_cancellation_uncertain"
		if !responseOK {
			out.Detail = "取消响应失败；必须核对订单状态，不要创建新测试订单"
		}
		if err := saveJSON(receiptPath(cfg, c.Session, out.TargetDate), out); err != nil {
			return out, err
		}
		return out, &Fault{Kind: "test_cancellation_uncertain"}
	}
	out.Status = "test_cancelled"
	out.Detail = "测试订单已核验并取消，未支付"
	return out, saveJSON(receiptPath(cfg, c.Session, out.TargetDate), out)
}

func rejectionKind(message string) string {
	switch {
	case strings.Contains(message, "验证码"):
		return "booking_captcha_rejected"
	case strings.Contains(message, "下单速度过快"):
		return "order_rate_limited"
	case strings.Contains(message, "已被预订") || strings.Contains(message, "已经被预订") || strings.Contains(message, "已被预定") || strings.Contains(message, "已被占用"):
		return "slot_taken"
	case strings.Contains(message, "禁止预订天数超"):
		return "date_not_released"
	case strings.Contains(message, "访问人数较多") || strings.Contains(message, "系统繁忙"):
		return "order_busy"
	default:
		return "order_rejected"
	}
}
