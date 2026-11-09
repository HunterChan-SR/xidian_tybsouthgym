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

func TestCaptchaSolverConfiguration(t *testing.T) {
	cfg := config(t)
	cfg.CaptchaSolverPersistent = true
	if cfg.Validate() == nil {
		t.Fatal("persistent solver without a command accepted")
	}
	cfg.CaptchaSolverCommand = []string{"oracle"}
	if cfg.Validate() == nil {
		t.Fatal("solver configured with captcha disabled")
	}
	cfg.BookingCaptcha = true
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.CaptchaSolverCommand = []string{" "}
	if cfg.Validate() == nil {
		t.Fatal("empty solver executable accepted")
	}
}
