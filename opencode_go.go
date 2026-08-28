package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	opencodeGoDashboardURLPrefix = "https://opencode.ai/workspace/"
	opencodeGoDashboardURLSuffix = "/go"
	opencodeGoUserAgent          = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Gecko/20100101 Firefox/148.0"
)

type opencodeGoUsageProvider struct {
	accountsPath string
}

func (opencodeGoUsageProvider) ID() string { return "opencode-go" }

func (p opencodeGoUsageProvider) collect(client *http.Client) ([]usageRow, error) {
	store, err := loadAccountsOrEmpty(p.accountsPath)
	if err != nil {
		return nil, err
	}

	rows := collectOpenCodeGoStoredAccounts(store.Accounts, client, queryOpenCodeGoQuota, time.Now())
	if len(rows) > 0 {
		return rows, nil
	}

	cfg, err := resolveOpenCodeGoConfig()
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, nil
	}

	result, err := queryOpenCodeGoQuota(cfg.WorkspaceID, cfg.AuthCookie, client)
	if err != nil {
		return nil, err
	}

	return []usageRow{opencodeGoUsageRow(cfg.WorkspaceID, result, time.Now())}, nil
}

type opencodeGoQuerier func(workspaceID, authCookie string, client *http.Client) (*opencodeGoResult, error)

func collectOpenCodeGoStoredAccounts(accounts []storedAccount, client *http.Client, query opencodeGoQuerier, now time.Time) []usageRow {
	var rows []usageRow
	for _, acc := range accounts {
		if normalizeAuthType(acc.AuthData.Type) != "opencode-go" {
			continue
		}
		if acc.AuthData.WorkspaceID == nil || acc.AuthData.AuthCookie == nil {
			continue
		}
		name := strings.TrimSpace(acc.Name)
		if name == "" {
			name = *acc.AuthData.WorkspaceID
		}
		result, err := query(*acc.AuthData.WorkspaceID, *acc.AuthData.AuthCookie, client)
		if err != nil {
			rows = append(rows, opencodeGoNAUsageRow(name))
			continue
		}
		row := opencodeGoUsageRow(name, result, now)
		rows = append(rows, row)
	}
	return rows
}

func opencodeGoNAUsageRow(name string) usageRow {
	row := opencodeGoUsageRow(name, &opencodeGoResult{}, time.Now())
	for _, label := range []string{"5H", "WEEKLY", "MONTHLY"} {
		row.Windows = append(row.Windows, usageWindow{Label: label, Summary: "n/a"})
	}
	return row
}

type opencodeGoConfig struct {
	WorkspaceID string
	AuthCookie  string
	Source      string
}

func resolveOpenCodeGoConfig() (*opencodeGoConfig, error) {
	workspaceID := strings.TrimSpace(os.Getenv("OPENCODE_GO_WORKSPACE_ID"))
	authCookie := strings.TrimSpace(os.Getenv("OPENCODE_GO_AUTH_COOKIE"))
	if workspaceID != "" || authCookie != "" {
		if workspaceID == "" || authCookie == "" {
			missing := "OPENCODE_GO_AUTH_COOKIE"
			if workspaceID == "" {
				missing = "OPENCODE_GO_WORKSPACE_ID"
			}
			return nil, fmt.Errorf("missing %s (source: env)", missing)
		}
		return &opencodeGoConfig{WorkspaceID: workspaceID, AuthCookie: authCookie, Source: "env"}, nil
	}

	for _, path := range opencodeGoConfigCandidates() {
		workspaceID, authCookie, err := readOpenCodeGoConfigFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("invalid config (%s): %v", path, err)
		}
		if workspaceID == "" || authCookie == "" {
			missing := "authCookie"
			if workspaceID == "" {
				missing = "workspaceId"
			}
			return nil, fmt.Errorf("missing %s (source: %s)", missing, path)
		}
		return &opencodeGoConfig{WorkspaceID: workspaceID, AuthCookie: authCookie, Source: path}, nil
	}

	return nil, nil
}

func readOpenCodeGoConfigFile(path string) (string, string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	var parsed struct {
		WorkspaceID string `json:"workspaceId"`
		AuthCookie  string `json:"authCookie"`
	}
	if err := json.Unmarshal(content, &parsed); err != nil {
		return "", "", fmt.Errorf("failed to parse JSON: %v", err)
	}
	return strings.TrimSpace(parsed.WorkspaceID), strings.TrimSpace(parsed.AuthCookie), nil
}

func opencodeGoConfigCandidates() []string {
	dirs := opencodeConfigDirs()
	paths := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		paths = append(paths, filepath.Join(dir, "opencode-quota", "opencode-go.json"))
	}
	return paths
}

func opencodeConfigDirs() []string {
	home, _ := os.UserHomeDir()
	if configured := strings.TrimSpace(os.Getenv("OPENCODE_CONFIG_DIR")); configured != "" {
		if !filepath.IsAbs(configured) {
			configured = filepath.Join(filepath.Join(home, ".config", "opencode"), configured)
		}
		return []string{configured}
	}

	var dirs []string
	switch runtime.GOOS {
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			dirs = append(dirs, filepath.Join(appData, "opencode"))
		} else {
			dirs = append(dirs, filepath.Join(home, "AppData", "Roaming", "opencode"))
		}
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			dirs = append(dirs, filepath.Join(local, "opencode"))
		}
	case "darwin":
		dirs = append(dirs, filepath.Join(home, "Library", "Application Support", "opencode"))
		dirs = append(dirs, filepath.Join(home, ".config", "opencode"))
	default:
		dirs = append(dirs, filepath.Join(home, ".config", "opencode"))
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		dirs = append([]string{filepath.Join(xdg, "opencode")}, dirs...)
	}
	return dedupePaths(dirs)
}

func dedupePaths(paths []string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	return out
}

type opencodeGoWindow struct {
	UsagePercent     float64
	PercentRemaining float64
	ResetInSec       float64
	ResetTimeIso     string
}

type opencodeGoResult struct {
	Rolling *opencodeGoWindow
	Weekly  *opencodeGoWindow
	Monthly *opencodeGoWindow
}

func opencodeGoUsageRow(workspaceID string, result *opencodeGoResult, now time.Time) usageRow {
	row := usageRow{
		Name:     workspaceID,
		Email:    "-",
		Plan:     "OpenCode Go",
		Windows:  []usageWindow{},
		SortName: strings.ToLower(workspaceID),
	}
	if result.Rolling != nil {
		row.Windows = append(row.Windows, opencodeGoWindowUsage("5H", result.Rolling, now))
	}
	if result.Weekly != nil {
		row.Windows = append(row.Windows, opencodeGoWindowUsage("WEEKLY", result.Weekly, now))
	}
	if result.Monthly != nil {
		row.Windows = append(row.Windows, opencodeGoWindowUsage("MONTHLY", result.Monthly, now))
	}
	return row
}

func opencodeGoWindowUsage(label string, window *opencodeGoWindow, now time.Time) usageWindow {
	used := percentValue(window.UsagePercent)
	summary := fmt.Sprintf("%.0f%% used / %.0f%% left", used, 100-used)
	resetAt := now.Add(time.Duration(window.ResetInSec) * time.Second).Unix()
	relative, absolute := resetTimesText(&resetAt, now)
	if relative != "-" && absolute != "-" {
		summary = fmt.Sprintf("%s - resets in %s (%s)", summary, relative, absolute)
	}
	return usageWindow{Label: label, Summary: summary, UsedPercent: &used}
}

type opencodeGoScrapedWindow struct {
	UsagePercent float64
	ResetInSec   float64
}

var opencodeGoNumberPattern = `(-?\d+(?:\.\d+)?)`

var opencodeGoSSRPatterns = func() map[string][2]*regexp.Regexp {
	keys := []string{"rollingUsage", "weeklyUsage", "monthlyUsage"}
	out := make(map[string][2]*regexp.Regexp, len(keys))
	for _, key := range keys {
		pctFirst := regexp.MustCompile(
			key + `:\$R\[\d+\]=\{` + `[^}]*usagePercent:` + opencodeGoNumberPattern +
				`[^}]*resetInSec:` + opencodeGoNumberPattern + `[^}]*\}`)
		resetFirst := regexp.MustCompile(
			key + `:\$R\[\d+\]=\{` + `[^}]*resetInSec:` + opencodeGoNumberPattern +
				`[^}]*usagePercent:` + opencodeGoNumberPattern + `[^}]*\}`)
		out[key] = [2]*regexp.Regexp{pctFirst, resetFirst}
	}
	return out
}()

var (
	opencodeGoDataSlotItem  = regexp.MustCompile(`data-slot="usage-item"`)
	opencodeGoUsageLabelRe  = regexp.MustCompile(`data-slot="usage-label">([^<]+)<`)
	opencodeGoUsageValueRe  = regexp.MustCompile(`data-slot="usage-value">[^0-9]*(\d+(?:\.\d+)?)`)
	opencodeGoResetSlotRe   = regexp.MustCompile(`data-slot="(reset-time|reset-now)">([\s\S]*?)</span>`)
	opencodeGoResetsInRe    = regexp.MustCompile(`(?i)Resets?\s*in\s*`)
	opencodeGoDurationParts = []struct {
		pattern    *regexp.Regexp
		multiplier float64
	}{
		{regexp.MustCompile(`(\d+(?:\.\d+)?)\s*days?`), 86400},
		{regexp.MustCompile(`(\d+(?:\.\d+)?)\s*hours?`), 3600},
		{regexp.MustCompile(`(\d+(?:\.\d+)?)\s*minutes?`), 60},
		{regexp.MustCompile(`(\d+(?:\.\d+)?)\s*seconds?`), 1},
	}
)

func parseOpenCodeGoSSR(html, key string) *opencodeGoScrapedWindow {
	for i, pattern := range opencodeGoSSRPatterns[key] {
		match := pattern.FindStringSubmatch(html)
		if match == nil {
			continue
		}
		first, firstErr := strconv.ParseFloat(match[1], 64)
		second, secondErr := strconv.ParseFloat(match[2], 64)
		if firstErr != nil || secondErr != nil {
			continue
		}
		if i == 0 {
			return &opencodeGoScrapedWindow{UsagePercent: first, ResetInSec: second}
		}
		return &opencodeGoScrapedWindow{UsagePercent: second, ResetInSec: first}
	}
	return nil
}

func parseOpenCodeGoDataSlot(html string) map[string]opencodeGoScrapedWindow {
	result := make(map[string]opencodeGoScrapedWindow)

	items := opencodeGoDataSlotItem.Split(html, -1)
	for _, item := range items[1:] {
		labelMatch := opencodeGoUsageLabelRe.FindStringSubmatch(item)
		if labelMatch == nil {
			continue
		}
		label := strings.ToLower(strings.TrimSpace(labelMatch[1]))

		usageMatch := opencodeGoUsageValueRe.FindStringSubmatch(item)
		if usageMatch == nil {
			continue
		}
		usagePercent, err := strconv.ParseFloat(usageMatch[1], 64)
		if err != nil {
			continue
		}

		resetMatch := opencodeGoResetSlotRe.FindStringSubmatch(item)
		if resetMatch == nil {
			continue
		}
		var resetInSec float64
		if resetMatch[1] == "reset-now" {
			resetInSec = 0
		} else {
			clean := strings.NewReplacer("<!--$-->", "", "<!--/-->", "").Replace(resetMatch[2])
			clean = opencodeGoResetsInRe.ReplaceAllString(clean, "")
			value, ok := parseOpenCodeGoHumanTime(clean)
			if !ok {
				continue
			}
			resetInSec = value
		}

		var windowKey string
		switch {
		case strings.Contains(label, "rolling"):
			windowKey = "rolling"
		case strings.Contains(label, "weekly"):
			windowKey = "weekly"
		case strings.Contains(label, "monthly"):
			windowKey = "monthly"
		default:
			continue
		}
		result[windowKey] = opencodeGoScrapedWindow{UsagePercent: usagePercent, ResetInSec: resetInSec}
	}

	return result
}

func parseOpenCodeGoHumanTime(value string) (float64, bool) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.Join(strings.Fields(normalized), " ")
	switch normalized {
	case "reset-now", "reset now", "now", "resets now":
		return 0, true
	}

	var total float64
	found := false
	for _, part := range opencodeGoDurationParts {
		match := part.pattern.FindStringSubmatch(normalized)
		if match == nil {
			continue
		}
		value, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			continue
		}
		total += value * part.multiplier
		found = true
	}
	return total, found
}

func normalizeOpenCodeGoWindow(window opencodeGoScrapedWindow, now time.Time) *opencodeGoWindow {
	usagePercent := math.Max(0, window.UsagePercent)
	resetInSec := math.Max(0, window.ResetInSec)
	return &opencodeGoWindow{
		UsagePercent:     usagePercent,
		PercentRemaining: 100 - usagePercent,
		ResetInSec:       resetInSec,
		ResetTimeIso:     now.Add(time.Duration(resetInSec) * time.Second).UTC().Format(time.RFC3339),
	}
}

func queryOpenCodeGoQuota(workspaceID, authCookie string, client *http.Client) (*opencodeGoResult, error) {
	dashboardURL := opencodeGoDashboardURLPrefix + url.PathEscape(workspaceID) + opencodeGoDashboardURLSuffix
	return queryOpenCodeGoQuotaAt(dashboardURL, authCookie, client)
}

func queryOpenCodeGoQuotaAt(dashboardURL, authCookie string, client *http.Client) (*opencodeGoResult, error) {
	req, err := http.NewRequest(http.MethodGet, dashboardURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", opencodeGoUserAgent)
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Cookie", "auth="+authCookie)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("OpenCode Go dashboard error %s: %s", resp.Status, sanitizeMessage(string(body), 300))
	}

	html := string(body)

	rolling := parseOpenCodeGoSSR(html, "rollingUsage")
	weekly := parseOpenCodeGoSSR(html, "weeklyUsage")
	monthly := parseOpenCodeGoSSR(html, "monthlyUsage")

	if rolling == nil && weekly == nil && monthly == nil {
		dataSlot := parseOpenCodeGoDataSlot(html)
		if value, ok := dataSlot["rolling"]; ok {
			rolling = &opencodeGoScrapedWindow{UsagePercent: value.UsagePercent, ResetInSec: value.ResetInSec}
		}
		if value, ok := dataSlot["weekly"]; ok {
			weekly = &opencodeGoScrapedWindow{UsagePercent: value.UsagePercent, ResetInSec: value.ResetInSec}
		}
		if value, ok := dataSlot["monthly"]; ok {
			monthly = &opencodeGoScrapedWindow{UsagePercent: value.UsagePercent, ResetInSec: value.ResetInSec}
		}
	}

	if rolling == nil && weekly == nil && monthly == nil {
		return nil, fmt.Errorf("could not parse any known OpenCode Go dashboard usage windows (rollingUsage, weeklyUsage, monthlyUsage)")
	}

	now := time.Now()
	result := &opencodeGoResult{}
	if rolling != nil {
		result.Rolling = normalizeOpenCodeGoWindow(*rolling, now)
	}
	if weekly != nil {
		result.Weekly = normalizeOpenCodeGoWindow(*weekly, now)
	}
	if monthly != nil {
		result.Monthly = normalizeOpenCodeGoWindow(*monthly, now)
	}
	return result, nil
}

func sanitizeMessage(text string, maxLength int) string {
	sanitized := strings.Join(strings.Fields(text), " ")
	if sanitized == "" {
		sanitized = "unknown"
	}
	if len(sanitized) > maxLength {
		sanitized = sanitized[:maxLength]
	}
	return sanitized
}
