package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

func runAccountsAddOpenCodeGo(args []string) int {
	fs := flag.NewFlagSet("accounts add-opencode-go", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	accountsPath := fs.String("accounts-file", defaultAccountsPath(), "path to accounts.json")
	name := fs.String("name", "", "display name for the workspace")
	workspaceID := fs.String("workspace-id", "", "OpenCode Go workspace ID")
	authCookie := fs.String("auth-cookie", "", "OpenCode Go auth cookie")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "accounts add-opencode-go does not take positional arguments")
		return 2
	}

	workspaceID = strPtr(strings.TrimSpace(*workspaceID))
	authCookie = strPtr(strings.TrimSpace(*authCookie))
	if *workspaceID == "" || *authCookie == "" {
		fmt.Fprintln(os.Stderr, "error: --workspace-id and --auth-cookie are required")
		return 2
	}

	store, err := loadAccountsOrEmpty(*accountsPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	displayName := strings.TrimSpace(*name)
	if displayName == "" {
		displayName = *workspaceID
	}

	for i := range store.Accounts {
		acc := &store.Accounts[i]
		if normalizeAuthType(acc.AuthData.Type) != "opencode-go" {
			continue
		}
		if acc.AuthData.WorkspaceID != nil && *acc.AuthData.WorkspaceID == *workspaceID {
			if displayName != *workspaceID {
				acc.Name = displayName
			}
			acc.AuthData.AuthCookie = authCookie
			if err := saveAccounts(*accountsPath, store); err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				return 1
			}
			fmt.Printf("Updated account %q (%s).\n", acc.Name, acc.ID)
			return 0
		}
	}

	store.Accounts = append(store.Accounts, storedAccount{
		ID:   newAccountID(),
		Name: displayName,
		AuthData: authData{
			Type:        "opencode-go",
			WorkspaceID: workspaceID,
			AuthCookie:  authCookie,
		},
	})
	if err := saveAccounts(*accountsPath, store); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	fmt.Printf("Added account %q (%s).\n", displayName, store.Accounts[len(store.Accounts)-1].ID)
	return 0
}
