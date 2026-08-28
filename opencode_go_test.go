package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const opencodeGoSSRFixture = `<script>
(() => { let x = 1; })();
$R[3]={rollingUsage:$R[4]={label:"Rolling Usage",usagePercent:23.5,resetInSec:54321},weeklyUsage:$R[5]={label:"Weekly Usage",resetInSec:302400,usagePercent:10},monthlyUsage:$R[6]={label:"Monthly Usage",usagePercent:42.7,resetInSec:1209600}}
</script>`

const opencodeGoDataSlotFixture = `<html><body>
<div data-slot="usage-item">
  <div data-slot="usage-label">Rolling Usage</div>
  <div data-slot="usage-value">23.5%</div>
  <span data-slot="reset-time">Resets in <!--$-->1 hour 56 minutes<!--/--></span>
</div>
<div data-slot="usage-item">
  <div data-slot="usage-label">Weekly Usage</div>
  <div data-slot="usage-value">10%</div>
  <span data-slot="reset-now">Resets now</span>
</div>
<div data-slot="usage-item">
  <div data-slot="usage-label">Monthly Usage</div>
  <div data-slot="usage-value">42%</div>
  <span data-slot="reset-time">Resets in 6 days 2 hours</span>
</div>
</body></html>`

func TestParseOpenCodeGoSSR(t *testing.T) {
	rolling := parseOpenCodeGoSSR(opencodeGoSSRFixture, "rollingUsage")
	if rolling == nil || rolling.UsagePercent != 23.5 || rolling.ResetInSec != 54321 {
		t.Fatalf("rolling = %#v, want usage 23.5 reset 54321", rolling)
	}

	weekly := parseOpenCodeGoSSR(opencodeGoSSRFixture, "weeklyUsage")
	if weekly == nil || weekly.UsagePercent != 10 || weekly.ResetInSec != 302400 {
		t.Fatalf("weekly = %#v, want usage 10 reset 302400", weekly)
	}

	monthly := parseOpenCodeGoSSR(opencodeGoSSRFixture, "monthlyUsage")
	if monthly == nil || monthly.UsagePercent != 42.7 || monthly.ResetInSec != 1209600 {
		t.Fatalf("monthly = %#v, want usage 42.7 reset 1209600", monthly)
	}

	if got := parseOpenCodeGoSSR("<html>nothing here</html>", "rollingUsage"); got != nil {
		t.Fatalf("expected nil for missing window, got %#v", got)
	}
}

func TestParseOpenCodeGoDataSlot(t *testing.T) {
	got := parseOpenCodeGoDataSlot(opencodeGoDataSlotFixture)

	rolling, ok := got["rolling"]
	if !ok || rolling.UsagePercent != 23.5 || rolling.ResetInSec != 1*3600+56*60 {
		t.Fatalf("rolling = %#v ok=%v, want usage 23.5 reset 6960", rolling, ok)
	}

	weekly, ok := got["weekly"]
	if !ok || weekly.UsagePercent != 10 || weekly.ResetInSec != 0 {
		t.Fatalf("weekly = %#v ok=%v, want usage 10 reset 0", weekly, ok)
	}

	monthly, ok := got["monthly"]
	if !ok || monthly.UsagePercent != 42 || monthly.ResetInSec != 6*86400+2*3600 {
		t.Fatalf("monthly = %#v ok=%v, want usage 42 reset 525600", monthly, ok)
	}
}

func TestParseOpenCodeGoHumanTime(t *testing.T) {
	cases := []struct {
		input string
		secs  float64
		found bool
	}{
		{"1 hour 56 minutes", 1*3600 + 56*60, true},
		{"6 days 2 hours", 6*86400 + 2*3600, true},
		{"26 days 17 hours", 26*86400 + 17*3600, true},
		{"45 seconds", 45, true},
		{"reset now", 0, true},
		{"RESETS NOW", 0, true},
		{"garbage", 0, false},
		{"", 0, false},
	}
	for _, tc := range cases {
		secs, found := parseOpenCodeGoHumanTime(tc.input)
		if found != tc.found || (found && secs != tc.secs) {
			t.Fatalf("parseOpenCodeGoHumanTime(%q) = %v,%v want %v,%v", tc.input, secs, found, tc.secs, tc.found)
		}
	}
}

func TestQueryOpenCodeGoQuotaAt_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Cookie"); got != "auth=test-cookie" {
			t.Fatalf("Cookie = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got == "" {
			t.Fatalf("expected User-Agent header")
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(opencodeGoSSRFixture))
	}))
	defer server.Close()

	result, err := queryOpenCodeGoQuotaAt(server.URL, "test-cookie", server.Client())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Rolling == nil || result.Weekly == nil || result.Monthly == nil {
		t.Fatalf("expected all three windows, got %#v", result)
	}
	if result.Rolling.UsagePercent != 23.5 || result.Rolling.PercentRemaining != 76.5 {
		t.Fatalf("rolling = %#v", result.Rolling)
	}
	if !strings.HasPrefix(result.Rolling.ResetTimeIso, "20") {
		t.Fatalf("ResetTimeIso = %q", result.Rolling.ResetTimeIso)
	}
}

func TestQueryOpenCodeGoQuotaAt_DataSlotFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(opencodeGoDataSlotFixture))
	}))
	defer server.Close()

	result, err := queryOpenCodeGoQuotaAt(server.URL, "test-cookie", server.Client())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Rolling == nil || result.Weekly == nil || result.Monthly == nil {
		t.Fatalf("expected all three windows, got %#v", result)
	}
	if result.Weekly.ResetInSec != 0 {
		t.Fatalf("weekly reset = %v, want 0", result.Weekly.ResetInSec)
	}
}

func TestQueryOpenCodeGoQuotaAt_Errors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "<html>login required</html>", http.StatusUnauthorized)
	}))
	defer server.Close()

	if _, err := queryOpenCodeGoQuotaAt(server.URL, "bad-cookie", server.Client()); err == nil {
		t.Fatalf("expected error for 401 response")
	}

	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>no usage data</html>"))
	}))
	defer server2.Close()

	if _, err := queryOpenCodeGoQuotaAt(server2.URL, "bad-cookie", server2.Client()); err == nil {
		t.Fatalf("expected error for unparseable page")
	}
}

func TestResolveOpenCodeGoConfigFromEnv(t *testing.T) {
	t.Setenv("OPENCODE_GO_WORKSPACE_ID", "ws_123")
	t.Setenv("OPENCODE_GO_AUTH_COOKIE", "cookie-abc")
	cfg, err := resolveOpenCodeGoConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil || cfg.WorkspaceID != "ws_123" || cfg.AuthCookie != "cookie-abc" || cfg.Source != "env" {
		t.Fatalf("cfg = %#v", cfg)
	}
}

func TestResolveOpenCodeGoConfigFromEnvPartial(t *testing.T) {
	t.Setenv("OPENCODE_GO_WORKSPACE_ID", "ws_123")
	t.Setenv("OPENCODE_GO_AUTH_COOKIE", "")
	_, err := resolveOpenCodeGoConfig()
	if err == nil || !strings.Contains(err.Error(), "OPENCODE_GO_AUTH_COOKIE") {
		t.Fatalf("expected missing env var error, got %v", err)
	}
}

func TestOpenCodeGoUsageRow(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	result := &opencodeGoResult{
		Rolling: &opencodeGoWindow{UsagePercent: 23.5, ResetInSec: 7200},
		Weekly:  &opencodeGoWindow{UsagePercent: 10, ResetInSec: 3600},
	}

	row := opencodeGoUsageRow("ws_123", result, now)
	if row.Name != "ws_123" || row.Plan != "OpenCode Go" {
		t.Fatalf("row identity = %#v", row)
	}
	if len(row.Windows) != 2 {
		t.Fatalf("windows = %#v, want 2", row.Windows)
	}
	if row.Windows[0].Label != "5H" || !strings.Contains(row.Windows[0].Summary, "24% used / 76% left") {
		t.Fatalf("rolling window = %#v", row.Windows[0])
	}
	if row.Windows[1].Label != "WEEKLY" {
		t.Fatalf("weekly window = %#v", row.Windows[1])
	}
	if row.ResetCredits != "" {
		t.Fatalf("expected no reset credits, got %q", row.ResetCredits)
	}
}

func TestBuildTableMixedProviders(t *testing.T) {
	used := 23.5
	used50, used10 := 50.0, 10.0
	rows := []usageRow{
		{
			Name:     "OpenCode",
			Email:    "-",
			Plan:     "OpenCode Go",
			Windows:  []usageWindow{{Label: "5H", Summary: "24% used / 76% left", UsedPercent: &used}},
			SortName: "opencode",
		},
		{
			Name:         "My Account",
			Email:        "me@example.com",
			Plan:         "plus",
			Windows:      []usageWindow{{Label: "5H", Summary: "50% used / 50% left", UsedPercent: &used50}, {Label: "WEEKLY", Summary: "10% used / 90% left", UsedPercent: &used10}},
			ResetCredits: "2",
			SortName:     "my account",
		},
	}

	table := buildTable(rows)
	if !strings.Contains(table, "ACCOUNT") || !strings.Contains(table, "5H") ||
		!strings.Contains(table, "WEEKLY") || !strings.Contains(table, "RESET CREDITS") {
		t.Fatalf("table missing columns:\n%s", table)
	}
	if strings.Contains(table, "MONTHLY") {
		t.Fatalf("MONTHLY column should be absent when no row provides it:\n%s", table)
	}
	if !strings.Contains(table, "OpenCode Go") || !strings.Contains(table, "-") {
		t.Fatalf("expected dash cells for missing windows:\n%s", table)
	}

	colored := applyUsageColors(table, rows)
	if !strings.Contains(colored, ansiAmber) {
		t.Fatalf("expected colorized usage:\n%s", colored)
	}
}

func TestRunAccountsAddOpenCodeGo(t *testing.T) {
	accountsPath := filepath.Join(t.TempDir(), "accounts.json")

	if code := runAccountsAddOpenCodeGo([]string{
		"--accounts-file", accountsPath,
		"--name", "My Workspace",
		"--workspace-id", "ws_abc",
		"--auth-cookie", "cookie-1",
	}); code != 0 {
		t.Fatalf("add returned %d", code)
	}

	store, err := loadAccounts(accountsPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(store.Accounts) != 1 {
		t.Fatalf("accounts = %d, want 1", len(store.Accounts))
	}
	acc := store.Accounts[0]
	if acc.Name != "My Workspace" || acc.AuthData.Type != "opencode-go" ||
		acc.AuthData.WorkspaceID == nil || *acc.AuthData.WorkspaceID != "ws_abc" ||
		acc.AuthData.AuthCookie == nil || *acc.AuthData.AuthCookie != "cookie-1" {
		t.Fatalf("stored account = %#v", acc)
	}

	if code := runAccountsAddOpenCodeGo([]string{
		"--accounts-file", accountsPath,
		"--workspace-id", "ws_abc",
		"--auth-cookie", "cookie-2",
	}); code != 0 {
		t.Fatalf("update returned %d", code)
	}
	store, _ = loadAccounts(accountsPath)
	if len(store.Accounts) != 1 || store.Accounts[0].AuthData.AuthCookie == nil || *store.Accounts[0].AuthData.AuthCookie != "cookie-2" {
		t.Fatalf("expected cookie upsert, got %#v", store.Accounts)
	}
}

func TestRunAccountsAddOpenCodeGoRequiresFlags(t *testing.T) {
	if code := runAccountsAddOpenCodeGo([]string{}); code != 2 {
		t.Fatalf("missing flags returned %d, want 2", code)
	}
	if code := runAccountsAddOpenCodeGo([]string{"--workspace-id", "ws_abc"}); code != 2 {
		t.Fatalf("missing cookie returned %d, want 2", code)
	}
}

func TestCollectOpenCodeGoStoredAccounts(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	accounts := []storedAccount{
		{
			ID:   "1",
			Name: "Primary WS",
			AuthData: authData{
				Type:        "opencode-go",
				WorkspaceID: strPtr("ws_ok"),
				AuthCookie:  strPtr("cookie-ok"),
			},
		},
		{
			ID:   "2",
			Name: "Broken WS",
			AuthData: authData{
				Type:        "opencode-go",
				WorkspaceID: strPtr("ws_bad"),
				AuthCookie:  strPtr("cookie-bad"),
			},
		},
		{
			ID:   "3",
			Name: "ChatGPT",
			AuthData: authData{
				Type:        "chatgpt",
				AccessToken: strPtr("token"),
			},
		},
		{
			ID: "4",
			AuthData: authData{
				Type: "opencode-go",
			},
		},
	}

	query := func(workspaceID, authCookie string, client *http.Client) (*opencodeGoResult, error) {
		if workspaceID == "ws_bad" {
			return nil, fmt.Errorf("dashboard error 401")
		}
		return &opencodeGoResult{
			Rolling: &opencodeGoWindow{UsagePercent: 23.5, ResetInSec: 7200},
		}, nil
	}

	rows := collectOpenCodeGoStoredAccounts(accounts, nil, query, now)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}

	if rows[0].Name != "Primary WS" || len(rows[0].Windows) != 1 {
		t.Fatalf("success row = %#v", rows[0])
	}
	if rows[1].Name != "Broken WS" || len(rows[1].Windows) != 3 {
		t.Fatalf("failure row = %#v", rows[1])
	}
	for _, win := range rows[1].Windows {
		if win.Summary != "n/a" {
			t.Fatalf("failure row window = %#v, want n/a", win)
		}
	}
}
