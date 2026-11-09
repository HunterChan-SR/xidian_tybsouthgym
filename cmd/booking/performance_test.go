package main

import (
	"encoding/json"
	"fmt"
	"testing"
)

// CPU-only benchmark for a whole returned day across twelve courts. It includes
// protocol parsing, candidate selection and the redacted observation payload.
func BenchmarkInventoryPipeline(b *testing.B) {
	cfg := Config{FieldTypeNo: "001", FieldPrefix: "YMQ", BeginTime: "19:00", EndTime: "21:00",
		PreferredFields: []string{"YMQ004"}, AllowOtherFields: true, MaxTotalCents: 2400}
	var slots []Slot
	for field := 1; field <= 12; field++ {
		for hour := 8; hour < 23; hour++ {
			slots = append(slots, slot(fmt.Sprintf("YMQ%03d", field), fmt.Sprintf("%02d:00", hour), fmt.Sprintf("%02d:00", hour+1), "12"))
		}
	}
	data, _ := json.Marshal(map[string]any{"type": 1, "resultdata": slots})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		parsed, err := parseSlots(data)
		if err != nil {
			b.Fatal(err)
		}
		candidates, err := choose(parsed, cfg, 10)
		if err != nil || len(candidates) == 0 {
			b.Fatal(err)
		}
		if _, err = json.Marshal(inventoryObservation(cfg, parsed, len(candidates), "synthetic")); err != nil {
			b.Fatal(err)
		}
	}
}
