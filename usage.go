package main

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

type codexUsageProvider struct {
	accountsPath string
}

func (codexUsageProvider) ID() string { return "codex" }

func codexAuthType(kind string) bool {
	switch normalizeAuthType(kind) {
	case "apikey", "chatgpt":
		return true
	default:
		return false
	}
}

func (p codexUsageProvider) collect(client *http.Client) ([]usageRow, error) {
	store, err := loadAccounts(p.accountsPath)
	if err != nil {
		return nil, err
	}

	type codexAccount struct {
		storeIndex int
		account    storedAccount
	}
	var codexAccounts []codexAccount
	for i := range store.Accounts {
		if codexAuthType(store.Accounts[i].AuthData.Type) {
			codexAccounts = append(codexAccounts, codexAccount{storeIndex: i, account: store.Accounts[i]})
		}
	}

	rows := make([]usageRow, len(codexAccounts))
	results := make(chan accountResult, len(codexAccounts))

	var wg sync.WaitGroup
	for i := range codexAccounts {
		wg.Add(1)
		storeIndex := codexAccounts[i].storeIndex
		acc := codexAccounts[i].account
		go func(idx, storeIndex int, account storedAccount) {
			defer wg.Done()

			row := usageRow{
				Name:         account.Name,
				Email:        valueOrDash(account.Email),
				Plan:         valueOrDash(account.PlanType),
				Windows:      naWindows(),
				ResetCredits: "n/a",
				SortName:     strings.ToLower(account.Name),
			}

			updated := account
			tokenRefreshed := false

			switch normalizeAuthType(account.AuthData.Type) {
			case "apikey":
			case "chatgpt":
				refreshedAcc, changed, refreshErr := ensureFreshTokens(account, client)
				if refreshErr != nil {
					results <- accountResult{Index: idx, StoreIndex: storeIndex, Row: row, Updated: updated}
					return
				}

				updated = refreshedAcc
				tokenRefreshed = changed

				usage, usageErr := fetchUsage(updated, client)
				if usageErr != nil {
					results <- accountResult{Index: idx, StoreIndex: storeIndex, Row: row, Updated: updated, TokenRefreshed: tokenRefreshed}
					return
				}

				row.Plan = firstNonEmpty(usage.PlanType, row.Plan)
				now := time.Now()
				row.Windows = []usageWindow{
					{Label: "5H", Summary: limitSummary(usage.RateLimit, true, now), UsedPercent: windowUsedPercent(usage.RateLimit, true)},
					{Label: "WEEKLY", Summary: limitSummary(usage.RateLimit, false, now), UsedPercent: windowUsedPercent(usage.RateLimit, false)},
				}
				resetCredits, resetCreditsErr := fetchResetCredits(updated, client)
				if resetCreditsErr != nil {
					row.ResetCredits = "unavailable"
				} else {
					row.ResetCredits = resetCreditsSummary(resetCredits, now)
				}
			}

			results <- accountResult{Index: idx, StoreIndex: storeIndex, Row: row, Updated: updated, TokenRefreshed: tokenRefreshed}
		}(i, storeIndex, acc)
	}

	wg.Wait()
	close(results)

	needsSave := false
	for result := range results {
		rows[result.Index] = result.Row
		if result.TokenRefreshed {
			store.Accounts[result.StoreIndex] = result.Updated
			needsSave = true
		}
	}

	if needsSave {
		if err := saveAccounts(p.accountsPath, store); err != nil {
			return nil, err
		}
	}

	return rows, nil
}

func naWindows() []usageWindow {
	return []usageWindow{
		{Label: "5H", Summary: "n/a"},
		{Label: "WEEKLY", Summary: "n/a"},
	}
}
