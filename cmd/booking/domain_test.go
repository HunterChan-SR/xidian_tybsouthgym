package main

import (
	"testing"
)

func TestMoney(t *testing.T) {
	for in, want := range map[string]int{"0": 0, "12": 1200, "12.0": 1200, "12.00": 1200, "0.01": 1} {
		got, e := money(in)
		if e != nil || got != want {
			t.Fatalf("%s => %d %v", in, got, e)
		}
	}
	for _, in := range []string{"-1", "NaN", "Inf", "1.001", "", "1e3", " 12", "1.", "1.+2"} {
		if _, e := money(in); e == nil {
			t.Fatalf("accepted %q", in)
		}
	}
}

func TestSingleDigitHours(t *testing.T) {
	got, e := normalizeTime("8:00")
	if e != nil || got != "08:00" {
		t.Fatal(got, e)
	}
}
