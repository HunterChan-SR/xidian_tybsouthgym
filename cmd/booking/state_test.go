package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStatePersistenceAndLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	for _, value := range []string{"pending", "success"} {
		if e := saveJSON(path, value); e != nil {
			t.Fatal(e)
		}
	}
	b, e := os.ReadFile(path)
	if e != nil || string(b) != "\"success\"" {
		t.Fatal(string(b), e)
	}
	unlock, e := acquire(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer unlock()
	if _, e = acquire(dir); e == nil {
		t.Fatal("duplicate runner allowed")
	}
}
