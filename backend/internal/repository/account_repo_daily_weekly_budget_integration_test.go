//go:build integration

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestDailyWeeklyBudgetConcurrentPersistenceAndStaleAdminEdit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := testEntClient(t)
	repo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	base := time.Date(2026, 11, 5, 5, 0, 0, 0, time.UTC)
	reset := base.Add(7 * 24 * time.Hour)
	account := mustCreateAccount(t, client, &service.Account{
		Name: "daily-budget-concurrency", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "test-token", "chatgpt_account_id": "test-account"},
		Extra:       map[string]any{"codex_7d_used_percent": 20.0, "codex_usage_updated_at": base.Format(time.RFC3339Nano), "codex_7d_reset_at": reset.Format(time.RFC3339Nano)},
	})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM scheduler_outbox
			WHERE account_id = $1 OR payload->'account_ids' @> jsonb_build_array($1::bigint)`, account.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id = $1", account.ID)
	})
	stale, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	stale.Name = "renamed during usage updates"
	stale.Extra[service.OpenAIDailyWeeklyBudgetStateKey] = map[string]any{"used_percent": 0}
	stale.Extra[service.OpenAIDailyWeeklyBudgetLimitKey] = 10.0
	var wg sync.WaitGroup
	errors := make(chan error, 41)
	for i := 1; i <= 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errors <- repo.UpdateExtra(ctx, account.ID, map[string]any{
				"codex_7d_used_percent":  float64(20 + i),
				"codex_usage_updated_at": base.Add(time.Duration(i) * time.Second).Format(time.RFC3339Nano),
				"codex_7d_reset_at":      reset.Format(time.RFC3339Nano),
			})
		}(i)
	}
	wg.Add(1)
	go func() { defer wg.Done(); errors <- repo.Update(ctx, stale) }()
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	got, err := repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	state, ok := service.ReadOpenAIDailyWeeklyBudgetState(got.Extra)
	require.True(t, ok)
	require.Equal(t, 40.0, state.UsedPercent, "concurrent callbacks cannot lose or duplicate weekly percentage deltas")
	require.Equal(t, 60.0, got.Extra["codex_7d_used_percent"])
	require.Equal(t, stale.Name, got.Name)
	require.Equal(t, 10.0, got.Extra[service.OpenAIDailyWeeklyBudgetLimitKey])
	// A fresh repository instance reads the same durable ledger after a restart.
	restarted := newAccountRepositoryWithSQL(client, integrationDB, nil)
	reloaded, err := restarted.GetByID(ctx, account.ID)
	require.NoError(t, err)
	reloadedState, ok := service.ReadOpenAIDailyWeeklyBudgetState(reloaded.Extra)
	require.True(t, ok)
	require.Equal(t, state, reloadedState)
	// Direct state injection through generic or bulk update must be ignored.
	require.NoError(t, repo.UpdateExtra(ctx, account.ID, map[string]any{service.OpenAIDailyWeeklyBudgetStateKey: nil}))
	_, err = repo.BulkUpdate(ctx, []int64{account.ID}, service.AccountBulkUpdate{Extra: map[string]any{
		service.OpenAIDailyWeeklyBudgetStateKey: map[string]any{"used_percent": 0}, "test_note": fmt.Sprint(account.ID),
	}})
	require.NoError(t, err)
	got, err = repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	after, ok := service.ReadOpenAIDailyWeeklyBudgetState(got.Extra)
	require.True(t, ok)
	require.Equal(t, state, after)
	// Refreshing credentials keeps the budget; replacing the upstream account
	// must discard the previous identity's ledger and canonical usage baseline.
	got.Credentials["access_token"] = "rotated-token"
	require.NoError(t, repo.Update(ctx, got))
	got, err = repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	afterRotation, ok := service.ReadOpenAIDailyWeeklyBudgetState(got.Extra)
	require.True(t, ok)
	require.Equal(t, state, afterRotation)
	got.Credentials["chatgpt_account_id"] = "replacement-account"
	require.NoError(t, repo.Update(ctx, got))
	got, err = repo.GetByID(ctx, account.ID)
	require.NoError(t, err)
	require.NotContains(t, got.Extra, service.OpenAIDailyWeeklyBudgetStateKey)
	require.NotContains(t, got.Extra, "codex_7d_used_percent")
}
