package main

import (
	"errors"
	"testing"
)

func TestRunWebUnknownCommand(t *testing.T) {
	err := runWeb([]string{"nope"})
	if err == nil || err.Error() != "unknown web command" {
		t.Fatalf("runWeb()=%v", err)
	}
}

func TestRunWebAdminUsageWithoutSubcommand(t *testing.T) {
	err := runWebAdmin(nil)
	if !errors.As(err, new(usageErr)) {
		t.Fatalf("runWebAdmin(nil)=%v want usageErr", err)
	}
}

func TestRunWebAdminHelp(t *testing.T) {
	if err := runWebAdmin([]string{"help"}); err != nil {
		t.Fatalf("runWebAdmin(help)=%v", err)
	}
	if err := runWebAdmin([]string{"--help"}); err != nil {
		t.Fatalf("runWebAdmin(--help)=%v", err)
	}
}

func TestRunWebAdminUnknownSubcommand(t *testing.T) {
	err := runWebAdmin([]string{"nope"})
	if !errors.As(err, new(usageErr)) {
		t.Fatalf("runWebAdmin(nope)=%v want usageErr", err)
	}
}

func TestWebAdminCreateMissingFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no flags", nil},
		{"username only", []string{"--username", "admin"}},
		{"stdin only", []string{"--password-stdin"}},
		{"extra arg", []string{"--username", "admin", "--password-stdin", "extra"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := webAdminCreate(tc.args)
			if !errors.As(err, new(usageErr)) {
				t.Fatalf("webAdminCreate()=%v want usageErr", err)
			}
		})
	}
}

func TestWebAdminResetPasswordMissingFlags(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no flags", nil},
		{"username only", []string{"--username", "admin"}},
		{"stdin only", []string{"--password-stdin"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := webAdminResetPassword(tc.args)
			if !errors.As(err, new(usageErr)) {
				t.Fatalf("webAdminResetPassword()=%v want usageErr", err)
			}
		})
	}
}

func TestWebAdminEnableDisableMissingUsername(t *testing.T) {
	for _, cmd := range []struct {
		name string
		run  func([]string) error
	}{
		{"enable", func(args []string) error { return webAdminSetEnabled(args, true) }},
		{"disable", func(args []string) error { return webAdminSetEnabled(args, false) }},
	} {
		t.Run(cmd.name, func(t *testing.T) {
			err := cmd.run(nil)
			if !errors.As(err, new(usageErr)) {
				t.Fatalf("%s()=%v want usageErr", cmd.name, err)
			}
		})
	}
}

func TestWebAdminListRejectsArguments(t *testing.T) {
	err := webAdminList([]string{"extra"})
	if !errors.As(err, new(usageErr)) {
		t.Fatalf("webAdminList(extra)=%v want usageErr", err)
	}
}
