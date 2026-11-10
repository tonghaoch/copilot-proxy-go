package main

import (
	"io"
	"strings"
	"testing"
)

func TestStartRejectsInvalidBudgetAndCacheFlags(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"--budget-spent-usd", "1"}, "requires --budget-usd"},
		{[]string{"--budget-reserved-usd", "1"}, "requires --budget-usd"},
		{[]string{"--budget-usd", "10", "--budget-reserved-usd", "1"}, "requires --budget-spent-usd"},
		{[]string{"--budget-usd", "-1"}, "budget must be"},
		{[]string{"--budget-usd", "10", "--budget-spent-usd", "-1"}, "prior estimated spend"},
		{[]string{"--budget-usd", "10", "--budget-spent-usd", "1", "--budget-reserved-usd", "2"}, "prior reserved amount"},
		{[]string{"--force-cache-ttl", "2h"}, "--force-cache-ttl must be"},
	}
	for _, tt := range tests {
		cmd := startCmd()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		cmd.SetArgs(tt.args)
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("args %v: got %v, want %q", tt.args, err, tt.want)
		}
	}
}
