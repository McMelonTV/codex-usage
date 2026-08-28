package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
)

type usageProvider interface {
	ID() string
	collect(client *http.Client) ([]usageRow, error)
}

func newProviders(accountsPath string) []usageProvider {
	return []usageProvider{
		codexUsageProvider{accountsPath: accountsPath},
		opencodeGoUsageProvider{accountsPath: accountsPath},
	}
}

func collectAllUsage(client *http.Client, providers []usageProvider) ([]usageRow, error) {
	var rows []usageRow
	var errs []error

	for _, provider := range providers {
		providerRows, err := provider.collect(client)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", provider.ID(), err))
			continue
		}
		rows = append(rows, providerRows...)
	}

	if len(rows) == 0 {
		if len(errs) > 0 {
			return nil, errors.Join(errs...)
		}
		return nil, nil
	}

	for _, err := range errs {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}

	sort.Slice(rows, func(i, j int) bool { return rows[i].SortName < rows[j].SortName })
	return rows, nil
}
