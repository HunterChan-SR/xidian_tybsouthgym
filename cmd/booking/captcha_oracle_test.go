package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Encode synthetic images with a unique outlined target for the real Python solver.
func oracleTestImages(t *testing.T) (string, string) {
	t.Helper()
	return oracleTestImageSizes(t, 300, 150, 50, 50)
}

func oracleTestImageSizes(t *testing.T, bgWidth, bgHeight, pieceWidth, pieceHeight int) (string, string) {
	t.Helper()
	var background, piece bytes.Buffer
	bg := image.NewRGBA(image.Rect(0, 0, bgWidth, bgHeight))
	part := image.NewNRGBA(image.Rect(0, 0, pieceWidth, pieceHeight))
	gray := image.NewUniform(color.RGBA{40, 40, 40, 255})
	draw.Draw(bg, bg.Bounds(), gray, image.Point{}, draw.Src)
	shape := image.Rect(5, 5, pieceWidth-5, pieceHeight-5)
	draw.Draw(part, shape, image.NewUniform(color.NRGBA{70, 100, 50, 255}), image.Point{}, draw.Src)
	outline := shape.Add(image.Pt((bgWidth-pieceWidth)/2, (bgHeight-pieceHeight)/2))
	draw.Draw(bg, outline, image.NewUniform(color.RGBA{225, 225, 225, 255}), image.Point{}, draw.Src)
	draw.Draw(bg, outline.Inset(1), gray, image.Point{}, draw.Src)
	if err := jpeg.Encode(&background, bg, nil); err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(&piece, part); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(background.Bytes()), base64.StdEncoding.EncodeToString(piece.Bytes())
}

func randomOracleTestCommand(t *testing.T) []string {
	t.Helper()
	python, err := exec.LookPath("python")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := filepath.Abs(filepath.Join("..", "..", "main.py"))
	if err != nil {
		t.Fatal(err)
	}
	return []string{python, "-X", "utf8", entry, "oracle-random"}
}

func oracleTestCommand(t *testing.T, mode string) []string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return []string{executable, "-test.run=^TestOracleProcess$", "--", mode}
}

// Run this test executable as a local oracle using synthetic challenges only.
func TestOracleProcess(t *testing.T) {
	args := os.Args
	if len(args) < 5 || args[len(args)-4] != "--" {
		return
	}
	mode, path := args[len(args)-3], args[len(args)-1]
	if args[len(args)-2] != "--request" {
		os.Exit(2)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		os.Exit(2)
	}
	var request struct {
		Account   string    `json:"account"`
		Deadline  time.Time `json:"deadline"`
		Challenge struct {
			Token string `json:"token"`
			BG    string `json:"bg"`
			Piece string `json:"piece"`
		} `json:"challenge"`
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &request) != nil || json.Unmarshal(data, &fields) != nil || len(fields) != 3 || !time.Now().Before(request.Deadline) {
		os.Exit(2)
	}
	if maxX, err := captchaMaxX(request.Challenge.BG, request.Challenge.Piece); err != nil || maxX != 250 {
		os.Exit(2)
	}
	reply := map[string]any{"token": request.Challenge.Token, "x": 123}
	switch mode {
	case "wrong-token":
		reply["token"] = "another-challenge"
	case "missing-x":
		delete(reply, "x")
	case "out-of-bounds":
		reply["x"] = 251
	case "cancel":
		reply["cancelled"] = true
	case "exit":
		fmt.Println("private-oracle-output")
		os.Exit(2)
	case "timeout":
		time.Sleep(10 * time.Second)
	case "no-reply":
		os.Exit(0)
	}
	if saveJSON(path+".reply", reply) != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestOracleSubmissionAndFailureCleanup(t *testing.T) {
	background, piece := oracleTestImages(t)
	for _, tc := range []struct {
		mode string
		kind string
	}{
		{"success", ""},
		{"wrong-token", "booking_captcha_protocol"},
		{"missing-x", "booking_captcha_protocol"},
		{"out-of-bounds", "booking_captcha_protocol"},
		{"cancel", "booking_captcha_not_completed"},
		{"exit", "booking_captcha_not_completed"},
		{"no-reply", "booking_captcha_not_completed"},
		{"timeout", "booking_captcha_not_completed"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			cfg := config(t)
			cfg.BookingCaptcha = true
			cfg.CaptchaSolverCommand = oracleTestCommand(t, tc.mode)
			orders := 0
			c := fake(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Cookie") != "UserId=test; session=secret" {
					t.Error("challenge and order must use the same session")
				}
				switch r.URL.Path {
				case "/Field/GetFieldSliderCaptcha":
					json.NewEncoder(w).Encode(map[string]any{"type": 1, "token": "order-challenge", "bg": background, "piece": piece})
				case "/Field/OrderField":
					orders++
					if r.URL.Query().Get("SliderToken") != "order-challenge" || r.URL.Query().Get("SliderX") != "123" {
						t.Error("oracle answer not attached to order")
					}
					fmt.Fprint(w, `{"type":1,"resultdata":"test-order"}`)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
				}
			})
			c.Name = "synthetic-account"
			baseURL, _ := url.Parse(c.Base)
			c.HTTP.Jar.SetCookies(baseURL, []*http.Cookie{{Name: "UserId", Value: "test"}, {Name: "session", Value: "secret"}})
			ctx := context.Background()
			if tc.mode == "timeout" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 500*time.Millisecond)
				defer cancel()
			}
			candidates, err := choose(pair("YMQ004", "12"), cfg, 10)
			if err != nil {
				t.Fatal(err)
			}
			_, err = guardedSubmit(ctx, c, cfg, candidates[0], "2026-11-10")
			kind := ""
			if err != nil {
				kind = faultKind(err)
			}
			if kind != tc.kind || (tc.mode == "success" && orders != 1) || (tc.mode != "success" && orders != 0) {
				t.Fatalf("kind=%q, orders=%d, error=%v", kind, orders, err)
			}
			if orders == 0 {
				if _, err := os.Stat(receiptPath(cfg, c.Session, "2026-11-10")); !os.IsNotExist(err) {
					t.Fatal("oracle failure left an order guard", err)
				}
			}
			files, err := filepath.Glob(filepath.Join(cfg.StateDir, "captcha", "*"))
			if err != nil || len(files) != 0 {
				t.Fatal("challenge files not cleaned up", files, err)
			}
		})
	}
}

func TestRandomOracleServerRejection(t *testing.T) {
	cfg := config(t)
	cfg.BookingCaptcha = true
	cfg.CaptchaSolverCommand = randomOracleTestCommand(t)
	background, piece := oracleTestImages(t)
	orders := 0
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Field/GetFieldSliderCaptcha":
			json.NewEncoder(w).Encode(map[string]any{"type": 1, "token": "random-test", "bg": background, "piece": piece})
		case "/Field/OrderField":
			orders++
			x, err := strconv.Atoi(r.URL.Query().Get("SliderX"))
			if err != nil || x < 0 || x > 250 || r.URL.Query().Get("SliderToken") != "random-test" {
				t.Error("invalid oracle answer attached to order")
			}
			fmt.Fprint(w, `{"type":3,"message":"验证码位置不正确","iscaptcha":1,"needreload":0}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	})
	candidates, _ := choose(pair("YMQ004", "12"), cfg, 10)
	out, err := guardedSubmit(context.Background(), c, cfg, candidates[0], "2026-11-10")
	if faultKind(err) != "booking_captcha_rejected" || orders != 1 || out.OrderID != "" {
		t.Fatal(out, err, orders)
	}
}
