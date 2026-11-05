package service

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func dailyBudgetObservation(at time.Time, percent float64, reset time.Time) map[string]any {
	return map[string]any{
		"codex_7d_used_percent":  percent,
		"codex_usage_updated_at": at.Format(time.RFC3339Nano),
		"codex_7d_reset_at":      reset.Format(time.RFC3339Nano),
	}
}

func applyDailyBudgetObservation(t *testing.T, extra map[string]any, at time.Time, percent float64, reset time.Time) OpenAIDailyWeeklyBudgetState {
	t.Helper()
	updates := dailyBudgetObservation(at, percent, reset)
	state, changed := FoldOpenAIDailyWeeklyBudget(extra, updates)
	require.True(t, changed)
	for key, value := range updates {
		extra[key] = value
	}
	extra[OpenAIDailyWeeklyBudgetStateKey] = state
	return state
}

func TestDailyWeeklyBudgetTracksWeeklyPoints(t *testing.T) {
	now := time.Date(2026, 11, 5, 5, 0, 0, 0, time.UTC)
	reset := now.Add(3 * 24 * time.Hour)
	extra := dailyBudgetObservation(now.Add(-time.Minute), 25, reset)
	state := applyDailyBudgetObservation(t, extra, now, 29, reset)
	require.Equal(t, 4.0, state.UsedPercent)
	state = applyDailyBudgetObservation(t, extra, now.Add(time.Minute), 35, reset)
	require.Equal(t, 10.0, state.UsedPercent)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: extra, Schedulable: true}
	ctx := withOpenAIQuotaAutoPauseSettings(context.Background(), OpsOpenAIAccountQuotaAutoPauseSettings{DailyWeeklyBudgetPercent: 10})
	paused, decision := shouldPauseOpenAIDailyWeeklyBudget(ctx, account, now.Add(2*time.Minute))
	require.True(t, paused)
	require.Equal(t, "daily_weekly_budget_exhausted", decision.reason)
	require.True(t, account.Schedulable, "the budget gate must not mutate account flags")
	paused, _ = shouldPauseOpenAIDailyWeeklyBudget(ctx, account, now.Add(24*time.Hour))
	require.False(t, paused, "next UTC+8 calendar day automatically reopens the budget")
}

func TestDailyWeeklyBudgetConfigurableCycles(t *testing.T) {
	ctx := withOpenAIQuotaAutoPauseSettings(context.Background(), OpsOpenAIAccountQuotaAutoPauseSettings{
		DailyWeeklyBudgetPercent: 10, WeeklyBudgetCycleHours: 24, WeeklyBudgetResetHour: 8,
	})
	cycle := ResolveOpenAIWeeklyBudgetCycle(ctx, nil)
	before := time.Date(2026, 11, 5, 7, 59, 0, 0, openAIDailyBudgetLocation)
	start, end := cycle.Window(before)
	require.Equal(t, time.Date(2026, 11, 4, 8, 0, 0, 0, openAIDailyBudgetLocation), start)
	require.Equal(t, time.Date(2026, 11, 5, 8, 0, 0, 0, openAIDailyBudgetLocation), end)
	justBefore, _ := cycle.Window(end.Add(-time.Nanosecond))
	require.Equal(t, start, justBefore, "the previous cycle includes the final nanosecond")
	exact, _ := cycle.Window(end)
	require.Equal(t, end, exact)

	extra := dailyBudgetObservation(start.Add(time.Minute), 20, start.Add(7*24*time.Hour))
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: extra}
	applyDailyBudgetObservation(t, extra, start.Add(2*time.Minute), 26, start.Add(7*24*time.Hour))
	// Calendar midnight does not refill an 08:00-to-08:00 budget.
	applyDailyBudgetObservation(t, extra, before, 30, start.Add(7*24*time.Hour))
	require.Equal(t, 10.0, openAIDailyWeeklyBudgetUsage(ctx, account, before))
	paused, _ := shouldPauseOpenAIDailyWeeklyBudget(ctx, account, before)
	require.True(t, paused)
	paused, _ = shouldPauseOpenAIDailyWeeklyBudget(ctx, account, end)
	require.False(t, paused, "reopen at the exact configured boundary without requiring a write")
	state := applyDailyBudgetObservation(t, extra, end.Add(time.Minute), 32, start.Add(7*24*time.Hour))
	require.Equal(t, 2.0, openAIDailyWeeklyBudgetUsage(ctx, account, end.Add(time.Minute)))
	require.Equal(t, 6.0, state.UsedPercent, "calendar-day display stays independent from the budget cycle")

	// Policy edits reuse retained history rather than resetting consumption.
	extra[OpenAIWeeklyBudgetCycleHoursKey] = 168.0
	extra[OpenAIWeeklyBudgetResetHourKey] = 0.0
	configured := ResolveOpenAIWeeklyBudgetCycle(ctx, account)
	require.Equal(t, OpenAIWeeklyBudgetCycle{Hours: 168, ResetHour: 0}, configured)
	weekStart, _ := configured.Window(end)
	expected := 0.0
	for _, bucket := range state.HourlyUsage {
		if !bucket.Hour.Before(weekStart) {
			expected += bucket.UsedPercent
		}
	}
	require.Equal(t, expected, openAIDailyWeeklyBudgetUsage(ctx, account, end.Add(time.Minute)))
	extra[OpenAIWeeklyBudgetCycleHoursKey] = nil
	extra[OpenAIWeeklyBudgetResetHourKey] = nil
	require.Equal(t, cycle, ResolveOpenAIWeeklyBudgetCycle(ctx, account))
}

func TestDailyWeeklyBudgetCustomCycleGatesAndAlignment(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 5, 13, 37, 0, 0, openAIDailyBudgetLocation)
	for _, hours := range []int{1, 5, 6, 12, 24, 48, 168} {
		for _, anchor := range []int{0, 8, 23} {
			cycle := OpenAIWeeklyBudgetCycle{Hours: hours, ResetHour: anchor}
			start, end := cycle.Window(now)
			require.False(t, start.After(now))
			require.True(t, end.After(now))
			require.Equal(t, time.Duration(hours)*time.Hour, end.Sub(start))
			account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{
				OpenAIDailyWeeklyBudgetLimitKey: 10, OpenAIWeeklyBudgetCycleHoursKey: hours, OpenAIWeeklyBudgetResetHourKey: anchor,
				OpenAIDailyWeeklyBudgetStateKey: OpenAIDailyWeeklyBudgetState{
					Day: "2026-11-05", ObservedAt: now, LastUsedPercent: 40, HistoryVersion: 1,
					HourlyUsage: []OpenAIWeeklyBudgetBucket{{Hour: start.Add(-time.Hour), UsedPercent: 50}, {Hour: start, UsedPercent: 10}},
				},
			}}
			paused, _ := shouldPauseOpenAIDailyWeeklyBudget(ctx, account, now)
			require.True(t, paused)
			require.Equal(t, 10.0, openAIDailyWeeklyBudgetUsage(ctx, account, now))
			paused, _ = shouldPauseOpenAIDailyWeeklyBudget(ctx, account, end)
			require.False(t, paused)
		}
	}
}

func TestDailyWeeklyBudgetHourlyRetentionAndLegacyCompatibility(t *testing.T) {
	at := time.Date(2026, 11, 5, 5, 0, 0, 0, time.UTC)
	state := OpenAIDailyWeeklyBudgetState{Day: "2026-11-05", UsedPercent: 9, LastUsedPercent: 30, ObservedAt: at}
	seedOpenAIWeeklyBudgetHistory(&state)
	seedOpenAIWeeklyBudgetHistory(&state)
	require.Len(t, state.HourlyUsage, 1, "legacy aggregate seeded once")
	require.Equal(t, 9.0, state.HourlyUsage[0].UsedPercent)
	for i := 0; i < 300; i++ {
		appendOpenAIWeeklyBudgetDelta(&state, at.Add(time.Duration(i)*time.Hour), 0.1)
		appendOpenAIWeeklyBudgetDelta(&state, at.Add(time.Duration(i)*time.Hour+time.Minute), 0.2)
	}
	require.Len(t, state.HourlyUsage, 169, "history stays bounded to seven days plus the current hour")
	require.Equal(t, 0.3, state.HourlyUsage[len(state.HourlyUsage)-1].UsedPercent)
	for _, field := range []struct {
		key string
		bad []any
	}{
		{OpenAIWeeklyBudgetCycleHoursKey, []any{0, -1, 169, 1.5, math.NaN(), "bad"}},
		{OpenAIWeeklyBudgetResetHourKey, []any{-1, 24, 2.5, math.Inf(1), "bad"}},
	} {
		for _, bad := range field.bad {
			require.Error(t, ValidateOpenAIDailyWeeklyBudgetExtra(map[string]any{field.key: bad}))
		}
		require.NoError(t, ValidateOpenAIDailyWeeklyBudgetExtra(map[string]any{field.key: nil}))
	}
	settings := defaultOpsAdvancedSettings()
	settings.OpenAIAccountQuotaAutoPause.WeeklyBudgetCycleHours = 169
	require.Error(t, validateOpsAdvancedSettings(settings))
	settings.OpenAIAccountQuotaAutoPause.WeeklyBudgetCycleHours = 0
	settings.OpenAIAccountQuotaAutoPause.WeeklyBudgetResetHour = 24
	require.Error(t, validateOpsAdvancedSettings(settings))
}

func TestDailyWeeklyBudgetCalendarBoundaryAndWeeklyReset(t *testing.T) {
	t.Run("UTC+8 midnight is independent from UTC midnight", func(t *testing.T) {
		at := time.Date(2026, 11, 5, 15, 59, 0, 0, time.UTC)
		reset := at.Add(24 * time.Hour)
		extra := dailyBudgetObservation(at.Add(-time.Minute), 20, reset)
		before := applyDailyBudgetObservation(t, extra, at, 30, reset)
		require.Equal(t, "2026-11-05", before.Day)
		require.Equal(t, 10.0, before.UsedPercent)
		after := applyDailyBudgetObservation(t, extra, at.Add(2*time.Minute), 32, reset)
		require.Equal(t, "2026-11-06", after.Day)
		require.Equal(t, 2.0, after.UsedPercent)
	})
	t.Run("weekly reset preserves today's already spent budget", func(t *testing.T) {
		at := time.Date(2026, 11, 5, 5, 0, 0, 0, time.UTC)
		reset := at.Add(time.Hour)
		extra := dailyBudgetObservation(at.Add(-time.Minute), 90, reset)
		applyDailyBudgetObservation(t, extra, at, 98, reset)
		after := applyDailyBudgetObservation(t, extra, reset.Add(time.Minute), 3, reset.Add(7*24*time.Hour))
		require.Equal(t, 11.0, after.UsedPercent)
		after = applyDailyBudgetObservation(t, extra, reset.Add(2*time.Minute), 4, reset.Add(7*24*time.Hour))
		require.Equal(t, 12.0, after.UsedPercent)
	})
	t.Run("early official reset also preserves today's budget", func(t *testing.T) {
		at := time.Date(2026, 11, 5, 5, 0, 0, 0, time.UTC)
		reset := at.Add(24 * time.Hour)
		extra := dailyBudgetObservation(at.Add(-time.Minute), 70, reset)
		applyDailyBudgetObservation(t, extra, at, 78, reset)
		after := applyDailyBudgetObservation(t, extra, at.Add(time.Minute), 3, reset.Add(6*24*time.Hour))
		require.Equal(t, 11.0, after.UsedPercent)
	})
}

func TestDailyWeeklyBudgetCorrectionsAndDelayedSnapshots(t *testing.T) {
	at := time.Date(2026, 11, 5, 5, 0, 0, 0, time.UTC)
	reset := at.Add(24 * time.Hour)
	extra := dailyBudgetObservation(at.Add(-time.Minute), 20, reset)
	applyDailyBudgetObservation(t, extra, at, 25, reset)
	correction := applyDailyBudgetObservation(t, extra, at.Add(time.Minute), 24, reset.Add(time.Second))
	require.Equal(t, 5.0, correction.UsedPercent)
	require.Equal(t, 25.0, correction.LastUsedPercent)
	recovered := applyDailyBudgetObservation(t, extra, at.Add(2*time.Minute), 26, reset)
	require.Equal(t, 6.0, recovered.UsedPercent)
	_, changed := FoldOpenAIDailyWeeklyBudget(extra, dailyBudgetObservation(at, 30, reset))
	require.False(t, changed, "late response cannot roll the ledger backwards")
	_, changed = FoldOpenAIDailyWeeklyBudget(extra, dailyBudgetObservation(at.Add(2*time.Minute), 26, reset))
	require.False(t, changed, "duplicate observation must be idempotent")
	for _, bad := range []float64{-1, 101, math.NaN(), math.Inf(1)} {
		_, changed = FoldOpenAIDailyWeeklyBudget(extra, dailyBudgetObservation(at.Add(3*time.Minute), bad, reset))
		require.False(t, changed)
	}
}

func TestDailyWeeklyBudgetElapsedResetNeedsWindowEvidence(t *testing.T) {
	at := time.Date(2026, 11, 5, 5, 0, 0, 0, time.UTC)
	reset := at.Add(time.Minute)
	extra := dailyBudgetObservation(at.Add(-time.Minute), 90, reset)
	applyDailyBudgetObservation(t, extra, at, 98, reset)
	stale := applyDailyBudgetObservation(t, extra, reset.Add(time.Second), 98, reset)
	require.Equal(t, 8.0, stale.UsedPercent, "elapsed reset alone cannot charge the old weekly usage again")
	propagating := applyDailyBudgetObservation(t, extra, reset.Add(2*time.Second), 99, reset)
	require.Equal(t, 9.0, propagating.UsedPercent)
	fresh := applyDailyBudgetObservation(t, extra, reset.Add(3*time.Second), 3, reset)
	require.Equal(t, 12.0, fresh.UsedPercent)
	require.True(t, fresh.WeeklyResetAt.IsZero(), "an old timestamp must not be consumed twice")
	stillStale := applyDailyBudgetObservation(t, extra, reset.Add(4*time.Second), 4, reset)
	require.Equal(t, 13.0, stillStale.UsedPercent)
	require.True(t, stillStale.WeeklyResetAt.IsZero())
	correction := applyDailyBudgetObservation(t, extra, reset.Add(5*time.Second), 3, reset)
	require.Equal(t, 13.0, correction.UsedPercent)
	next := applyDailyBudgetObservation(t, extra, reset.Add(6*time.Second), 5, reset.Add(7*24*time.Hour))
	require.Equal(t, 14.0, next.UsedPercent)
}

func TestDailyWeeklyBudgetDefaultsOverridesAndValidation(t *testing.T) {
	ctx := withOpenAIQuotaAutoPauseSettings(context.Background(), OpsOpenAIAccountQuotaAutoPauseSettings{DailyWeeklyBudgetPercent: 10})
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{}}
	require.Equal(t, 10.0, ResolveOpenAIDailyWeeklyBudgetLimit(ctx, account))
	account.Extra[OpenAIDailyWeeklyBudgetLimitKey] = 30.0
	require.Equal(t, 30.0, ResolveOpenAIDailyWeeklyBudgetLimit(ctx, account))
	account.Extra[OpenAIDailyWeeklyBudgetLimitKey] = 0.0
	require.Zero(t, ResolveOpenAIDailyWeeklyBudgetLimit(ctx, account))
	account.Extra[OpenAIDailyWeeklyBudgetLimitKey] = nil
	require.Equal(t, 10.0, ResolveOpenAIDailyWeeklyBudgetLimit(ctx, account))
	account.Type = AccountTypeAPIKey
	require.Zero(t, ResolveOpenAIDailyWeeklyBudgetLimit(ctx, account))
	for _, bad := range []any{-1, 101, "bad", math.NaN(), math.Inf(1)} {
		require.Error(t, ValidateOpenAIDailyWeeklyBudgetExtra(map[string]any{OpenAIDailyWeeklyBudgetLimitKey: bad}))
	}
	for _, good := range []any{nil, 0, 0.1, 10, 30, 100} {
		require.NoError(t, ValidateOpenAIDailyWeeklyBudgetExtra(map[string]any{OpenAIDailyWeeklyBudgetLimitKey: good}))
	}
	settings := defaultOpsAdvancedSettings()
	settings.OpenAIAccountQuotaAutoPause.DailyWeeklyBudgetPercent = math.NaN()
	require.Error(t, validateOpsAdvancedSettings(settings))
}

func dailyBudgetAccount(id int64, used, limit float64) Account {
	now := time.Now()
	return Account{ID: id, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"access_token": "test-token"},
		Extra: map[string]any{OpenAIDailyWeeklyBudgetLimitKey: limit, "privacy_mode": "training_disabled",
			OpenAIDailyWeeklyBudgetStateKey: OpenAIDailyWeeklyBudgetState{Day: now.In(openAIDailyBudgetLocation).Format("2006-01-02"), UsedPercent: used, LastUsedPercent: 40, ObservedAt: now}},
	}
}

func TestDailyWeeklyBudgetSchedulingAcrossEngines(t *testing.T) {
	for _, advanced := range []string{"true", "false"} {
		t.Run("advanced="+advanced, func(t *testing.T) {
			accounts := []Account{dailyBudgetAccount(91001, 10, 10), dailyBudgetAccount(91002, 8, 10), dailyBudgetAccount(91003, 1, 10)}
			cfg := &config.Config{}
			cfg.Gateway.OpenAIWS.LBTopK = 1
			cache := schedulerTestConcurrencyCache{loadMap: map[int64]*AccountLoadInfo{}, acquireResults: map[int64]bool{91001: true, 91002: true, 91003: true}}
			for _, account := range accounts {
				cache.loadMap[account.ID] = &AccountLoadInfo{AccountID: account.ID}
			}
			svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}, cache: &schedulerTestGatewayCache{}, cfg: cfg,
				rateLimitService: newOpenAIAdvancedSchedulerRateLimitService(advanced), concurrencyService: NewConcurrencyService(cache)}
			groupID := int64(901)
			selection, _, err := svc.SelectAccountWithScheduler(context.Background(), &groupID, "", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.NotNil(t, selection)
			require.Equal(t, int64(91003), selection.Account.ID, "least consumed daily budget must be preferred, even with TopK=1")
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
			for i := range accounts {
				accounts[i].Extra[OpenAIDailyWeeklyBudgetLimitKey] = 0.5
			}
			svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: accounts}
			_, _, err = svc.SelectAccountWithScheduler(context.Background(), &groupID, "", "", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
			require.ErrorIs(t, err, ErrNoAvailableAccounts, "all exhausted accounts must stop routing")
		})
	}
}

func TestDailyWeeklyBudgetReorderingPreservesExemptAndPriority(t *testing.T) {
	a, b, c := dailyBudgetAccount(1, 8, 10), dailyBudgetAccount(2, 0, 0), dailyBudgetAccount(3, 1, 10)
	d := dailyBudgetAccount(4, 0, 10)
	d.Priority = 100
	accounts := []*Account{&a, &b, &c, &d}
	prioritizeOpenAIDailyBudgetAccounts(context.Background(), accounts, false)
	require.Equal(t, []int64{3, 2, 1, 4}, []int64{accounts[0].ID, accounts[1].ID, accounts[2].ID, accounts[3].ID})
}

func TestDailyWeeklyBudgetStickyCannotBypassCap(t *testing.T) {
	for _, advanced := range []string{"true", "false"} {
		t.Run("advanced="+advanced, func(t *testing.T) {
			accounts := []Account{dailyBudgetAccount(92001, 10, 10), dailyBudgetAccount(92002, 3, 10)}
			cache := &schedulerTestGatewayCache{sessionBindings: map[string]int64{"budget-session": 92001}}
			svc := &OpenAIGatewayService{
				accountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}, cache: cache, cfg: &config.Config{},
				rateLimitService: newOpenAIAdvancedSchedulerRateLimitService(advanced), concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
			}
			groupID := int64(902)
			selection, _, err := svc.SelectAccountWithScheduler(context.Background(), &groupID, "", "budget-session", "gpt-5.1", nil, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.Equal(t, int64(92002), selection.Account.ID)
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
		})
	}
}

type dailyBudgetSnapshotRepo struct {
	AccountRepository
	account *Account
	updates chan map[string]any
}

func (r *dailyBudgetSnapshotRepo) GetByID(context.Context, int64) (*Account, error) {
	return r.account, nil
}
func (r *dailyBudgetSnapshotRepo) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	r.updates <- updates
	return nil
}

func TestDailyWeeklyBudgetBypassesDisplaySnapshotThrottle(t *testing.T) {
	account := dailyBudgetAccount(93001, 0, 10)
	repo := &dailyBudgetSnapshotRepo{account: &account, updates: make(chan map[string]any, 2)}
	svc := &OpenAIGatewayService{accountRepo: repo}
	window, reset, used := 10080, 3600, 25.0
	snapshot := &OpenAICodexUsageSnapshot{PrimaryUsedPercent: &used, PrimaryWindowMinutes: &window, PrimaryResetAfterSeconds: &reset}
	svc.updateCodexUsageSnapshot(context.Background(), account.ID, snapshot)
	svc.updateCodexUsageSnapshot(context.Background(), account.ID, snapshot)
	for i := 0; i < 2; i++ {
		select {
		case updates := <-repo.updates:
			require.Equal(t, 25.0, updates["codex_7d_used_percent"])
		case <-time.After(2 * time.Second):
			t.Fatal("enabled daily budget must persist each weekly observation, including within the display throttle")
		}
	}
}
