package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseAmount(t *testing.T) {
	cases := []struct {
		text string
		want float64
	}{
		{"微信支付收款到账 收款128.00元", 128.00},
		{"支付宝到账￥59.9", 59.9},
		{"无金额通知", 0},
	}
	for _, tc := range cases {
		if got := parseAmount(tc.text); got != tc.want {
			t.Fatalf("parseAmount(%q)=%v want %v", tc.text, got, tc.want)
		}
	}
}

func TestNormalizeChannel(t *testing.T) {
	cases := map[string]string{
		"com.tencent.mm":              "wxpay",
		"微信":                          "wxpay",
		"com.eg.android.alipaygphone": "alipay",
		"支付宝":                         "alipay",
		"custom":                      "custom",
	}
	for in, want := range cases {
		if got := normalizeChannel(in); got != want {
			t.Fatalf("normalizeChannel(%q)=%q want %q", in, got, want)
		}
	}
}

func TestSanitizePickStrategy(t *testing.T) {
	cases := map[string]string{
		"":              "least_amount",
		"least_amount":  "least_amount",
		"amount_lowest": "least_amount",
		"least_orders":  "least_orders",
		"order_lowest":  "least_orders",
		"round_robin":   "round_robin",
		"roundrobin":    "round_robin",
		"random":        "random",
		"bad":           "least_amount",
	}
	for in, want := range cases {
		if got := sanitizePickStrategy(in); got != want {
			t.Fatalf("sanitizePickStrategy(%q)=%q want %q", in, got, want)
		}
	}
}

func TestApplySummaryPercent(t *testing.T) {
	items := []SummaryPoint{{Amount: 0}, {Amount: 10, Count: 2}, {Amount: 100, Count: 4}}
	applySummaryPercent(items)
	if items[0].Percent != 0 || items[1].Percent != 10 || items[2].Percent != 100 {
		t.Fatalf("unexpected percent: %+v", items)
	}
	if items[0].Avg != 0 || items[1].Avg != 5 || items[2].Avg != 25 {
		t.Fatalf("unexpected avg: %+v", items)
	}
}

func TestParseADBDevices(t *testing.T) {
	output := `List of devices attached
R58M123ABC	device usb:1-1 product:o1qzcx model:SM_G9910 device:o1q transport_id:1
192.168.1.22:5555	device product:curtana model:Redmi_Note_9S device:curtana transport_id:2
ABCDEF	unauthorized usb:1-2 transport_id:3
`
	devices := parseADBDevices(output)
	if len(devices) != 3 {
		t.Fatalf("len=%d want 3: %+v", len(devices), devices)
	}
	if devices[0].Serial != "R58M123ABC" || devices[0].State != "device" || devices[0].Details["model"] != "SM_G9910" {
		t.Fatalf("unexpected first device: %+v", devices[0])
	}
	if devices[1].DisplayName() != "Redmi Note 9S / curtana" {
		t.Fatalf("unexpected display name: %q", devices[1].DisplayName())
	}
	if devices[2].State != "unauthorized" {
		t.Fatalf("unexpected unauthorized state: %+v", devices[2])
	}
}

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("NL_TEST_A=hello\nNL_TEST_B='world'\nexport NL_TEST_C=ok\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NL_TEST_A", "")
	t.Setenv("NL_TEST_B", "")
	t.Setenv("NL_TEST_C", "")
	loadDotEnv(path)
	if os.Getenv("NL_TEST_A") != "hello" || os.Getenv("NL_TEST_B") != "world" || os.Getenv("NL_TEST_C") != "ok" {
		t.Fatalf("dotenv not loaded: %q %q %q", os.Getenv("NL_TEST_A"), os.Getenv("NL_TEST_B"), os.Getenv("NL_TEST_C"))
	}
}
