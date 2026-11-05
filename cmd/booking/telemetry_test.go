package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTelemetryRedactsSecrets(t *testing.T) {
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("_") == "" {
			t.Error("missing cache buster")
		}
		if r.Header.Get("Cache-Control") == "" {
			t.Error("missing no-cache")
		}
		fmt.Fprint(w, "{\"type\":2,\"message\":\"bad secret-session-123\"}")
	})
	c.Session.Cookie = "UserId=account-123456; session=secret-session-123"
	c.EventLog = filepath.Join(t.TempDir(), "events.jsonl")
	_, e := c.get(context.Background(), "/test", nil)
	if e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(c.EventLog)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(b), "secret-session-123") || strings.Contains(string(b), "account-123456") {
		t.Fatal("secret leaked")
	}
	if !strings.Contains(string(b), "[redacted]") {
		t.Fatal(string(b))
	}
}

func TestInventoryEvidenceIncludesUnavailableButExcludesUnrelatedSlots(t *testing.T) {
	cfg := config(t)
	slots := []Slot{slot("YMQ004", "19:00", "20:00", "12"), slot("YMQ007", "20:00", "21:00", "12"), slot("YMQ009", "18:00", "19:00", "12"), slot("YMQ010", "19:00", "20:00", "12")}
	slots[3].FieldState = json.RawMessage("1")
	event := inventoryObservation(cfg, slots, 0, "alias")
	rows := event["availableSlots"].([]map[string]any)
	if len(rows) != 2 || rows[0]["field"] != "YMQ004" || rows[1]["field"] != "YMQ007" || event["fullWindowCandidates"] != 0 {
		t.Fatal(event)
	}
	if event["targetSlotCount"] != 3 || event["unavailableSlotCount"] != 1 {
		t.Fatal(event)
	}
	encoded, _ := json.Marshal(event)
	if strings.Contains(string(encoded), "YMQ009") || !strings.Contains(string(encoded), "YMQ010") || strings.Contains(string(encoded), "Cookie") {
		t.Fatal(string(encoded))
	}
}

func TestReadDiagnosticNeverCopiesUnexpectedPayload(t *testing.T) {
	c := &Client{EventLog: filepath.Join(t.TempDir(), "events.jsonl")}
	c.readFailure("/Field/GetVenueStateNew", []byte("{\"type\":\"private-id\",\"resultdata\":{\"Cookie\":\"secret-cookie\",\"MemberNo\":\"private-member\"},\"secret-key\":\"private-value\"}"), &Fault{Kind: "schema_changed", Reason: "result_not_slot_array"})
	b, err := os.ReadFile(c.EventLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-id", "Cookie", "secret-cookie", "private-member", "secret-key", "private-value"} {
		if strings.Contains(string(b), secret) {
			t.Fatal("untrusted payload copied to diagnostic")
		}
	}
	if !strings.Contains(string(b), "result_not_slot_array") || !strings.Contains(string(b), "object") {
		t.Fatal(string(b))
	}
}

func TestInventoryEvidenceKeepsUnknownPricesAndSanitizesStates(t *testing.T) {
	cfg := config(t)
	slots := pair("YMQ004", "12")
	slots[0].FinalPrice = nil
	slots[1].FieldState = json.RawMessage("\"private-state-value\"")
	event := inventoryObservation(cfg, slots, -1, "alias")
	if event["unknownPrices"] != 1 || event["targetSlotCount"] != 2 || event["unavailableSlotCount"] != 1 || len(event["availableSlots"].([]map[string]any)) != 0 {
		t.Fatal(event)
	}
	b, _ := json.Marshal(event)
	if strings.Contains(string(b), "private-state-value") {
		t.Fatal("unknown state leaked")
	}
}
