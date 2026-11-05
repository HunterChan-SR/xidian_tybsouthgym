package main

import (
	"testing"
)

func TestInvalidConfig(t *testing.T) {
	cfg := config(t)
	if e := cfg.Validate(); e != nil {
		t.Fatal(e)
	}
	for _, mutate := range []func(*Config){func(c *Config) { c.MaxTotalCents = 0 }, func(c *Config) { c.ReleaseTime = "wrong" }, func(c *Config) { c.EndTime = "18:00" }, func(c *Config) { c.PollSeconds = 0 }} {
		c := cfg
		mutate(&c)
		if e := c.Validate(); e == nil {
			t.Fatal(c)
		}
	}
}

func TestMultiAccountRequiresSharedGuard(t *testing.T) {
	cfg := config(t)
	cfg.Accounts = []Account{{Name: "one", CookieFile: "one.json"}, {Name: "two", CookieFile: "two.json"}}
	cfg.PoolID = ""
	if cfg.Validate() == nil {
		t.Fatal("multi-account pool without shared guard accepted")
	}
	cfg.PoolID = "shared"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.Accounts[1].Name = "one"
	if cfg.Validate() == nil {
		t.Fatal("duplicate alias accepted")
	}
}
