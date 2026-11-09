package service

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodexZeroLengthWindowIsAbsent(t *testing.T) {
	t.Parallel()
	for _, disabledSlot := range []string{"primary", "secondary"} {
		t.Run(disabledSlot, func(t *testing.T) {
			h := http.Header{}
			weeklySlot := "primary"
			if disabledSlot == "primary" {
				weeklySlot = "secondary"
			}
			h.Set("x-codex-"+weeklySlot+"-used-percent", "34")
			h.Set("x-codex-"+weeklySlot+"-window-minutes", "10080")
			h.Set("x-codex-"+weeklySlot+"-reset-after-seconds", "258262")
			h.Set("x-codex-"+disabledSlot+"-used-percent", "0")
			h.Set("x-codex-"+disabledSlot+"-window-minutes", "0")
			h.Set("x-codex-"+disabledSlot+"-reset-after-seconds", "0")
			snapshot := ParseCodexRateLimitHeaders(h)
			normalized := snapshot.Normalize()
			require.Nil(t, normalized.Used5hPercent)
			require.Nil(t, normalized.Reset5hSeconds)
			require.Nil(t, normalized.Window5hMinutes)
			require.NotNil(t, normalized.Used7dPercent)
			require.Equal(t, 34.0, *normalized.Used7dPercent)
			updates := buildCodexUsageExtraUpdates(snapshot, time.Now())
			for _, key := range []string{"codex_5h_used_percent", "codex_5h_window_minutes", "codex_5h_reset_after_seconds", "codex_5h_reset_at"} {
				value, ok := updates[key]
				require.True(t, ok, key)
				require.Nil(t, value, key)
			}
			// Normalization must preserve the original observed layout for diagnostics.
			if disabledSlot == "primary" {
				require.Equal(t, 0, *snapshot.PrimaryWindowMinutes)
			} else {
				require.Equal(t, 0, *snapshot.SecondaryWindowMinutes)
			}
		})
	}
}

func TestCachedZeroLengthCodexWindowDoesNotRenderOrPause(t *testing.T) {
	t.Parallel()
	for _, used := range []float64{0, 100} {
		t.Run(strconv.Itoa(int(used)), func(t *testing.T) {
			now := time.Now()
			account := &Account{ID: 12, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Extra: map[string]any{
				"codex_5h_used_percent": used, "codex_5h_window_minutes": 0,
				"codex_5h_reset_at":     now.Add(time.Hour).Format(time.RFC3339),
				"codex_7d_used_percent": 34.0, "codex_7d_window_minutes": 10080,
				"codex_7d_reset_at":          now.Add(72 * time.Hour).Format(time.RFC3339),
				"codex_primary_used_percent": 34.0, "codex_primary_window_minutes": 10080,
				"codex_secondary_used_percent": used, "codex_secondary_window_minutes": 0,
				"codex_usage_updated_at":  now.Format(time.RFC3339),
				"auto_pause_5h_threshold": 95.0, "auto_pause_7d_threshold": 95.0,
			}}
			repo := &accountUsageWindowStatsRepo{}
			svc := &AccountUsageService{usageLogRepo: repo}
			usage, err := svc.getOpenAIUsage(context.Background(), account, false)
			require.NoError(t, err)
			require.Nil(t, usage.FiveHour)
			require.NotNil(t, usage.SevenDay)
			require.Equal(t, 34.0, usage.SevenDay.Utilization)
			require.Equal(t, 1, repo.calls)
			require.False(t, shouldRefreshOpenAICodexSnapshot(account, usage, now))
			paused, _ := shouldAutoPauseOpenAIAccountByQuota(context.Background(), account)
			require.False(t, paused)
			require.InDelta(t, 0.66, openAIQuotaHeadroomFactor(account, now), 1e-9)
			// A real 300-minute window with 0% usage must remain visible.
			account.Extra["codex_5h_window_minutes"] = 300
			account.Extra["codex_5h_used_percent"] = 0.0
			require.NotNil(t, buildCodexUsageProgressFromExtra(account.Extra, "5h", now))
		})
	}
}
