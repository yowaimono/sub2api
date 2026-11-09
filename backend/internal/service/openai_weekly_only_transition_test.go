package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWeeklyOnlyCodexSnapshotTransition(t *testing.T) {
	t.Parallel()

	for _, source := range []string{"response_headers", "quota_api"} {
		for _, slot := range []string{"primary", "secondary"} {
			t.Run(source+"/"+slot, func(t *testing.T) {
				now := time.Now().UTC()
				account := &Account{
					Platform: PlatformOpenAI,
					Type:     AccountTypeOAuth,
					Extra: map[string]any{
						"auto_pause_5h_threshold":             95.0,
						"auto_pause_7d_threshold":             95.0,
						"codex_primary_used_percent":          22.0,
						"codex_primary_window_minutes":        10080,
						"codex_secondary_used_percent":        100.0,
						"codex_secondary_window_minutes":      300,
						"codex_secondary_reset_after_seconds": 3600,
						"codex_5h_used_percent":               100.0,
						"codex_5h_window_minutes":             300,
						"codex_5h_reset_after_seconds":        3600,
						"codex_5h_reset_at":                   now.Add(time.Hour).Format(time.RFC3339Nano),
						"codex_usage_updated_at":              now.Format(time.RFC3339Nano),
					},
				}
				paused, decision := shouldAutoPauseOpenAIAccountByQuota(context.Background(), account)
				require.True(t, paused)
				require.Equal(t, "5h", decision.window)

				var updates map[string]any
				if source == "response_headers" {
					headers := http.Header{}
					headers.Set("x-codex-"+slot+"-used-percent", "22")
					headers.Set("x-codex-"+slot+"-window-minutes", "10080")
					headers.Set("x-codex-"+slot+"-reset-after-seconds", "345600")
					updates = buildCodexUsageExtraUpdates(ParseCodexRateLimitHeaders(headers), now)
				} else {
					window := &OpenAIRateLimitWindow{UsedPercent: 22, LimitWindowSeconds: 604800, ResetAfterSeconds: 345600}
					rateLimit := &OpenAIRateLimit{Allowed: true}
					if slot == "primary" {
						rateLimit.PrimaryWindow = window
					} else {
						rateLimit.SecondaryWindow = window
					}
					updates = buildOpenAIAutoResetUsageUpdates(&OpenAIQuotaUsage{RateLimit: rateLimit}, now)
				}
				// Model the repository's JSONB merge, including explicit nulls,
				// and a subsequent JSON read back into the scheduler snapshot.
				mergeAccountExtra(account, updates)
				encoded, err := json.Marshal(account.Extra)
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(encoded, &account.Extra))
				usage := &UsageInfo{FiveHour: &UsageProgress{Utilization: 100}}
				applyExtraToUsage(usage, account.Extra, now)
				require.Nil(t, usage.FiveHour)
				require.NotNil(t, usage.SevenDay)
				require.Equal(t, 22.0, usage.SevenDay.Utilization)
				require.False(t, shouldRefreshOpenAICodexSnapshot(account, usage, now))
				paused, _ = shouldAutoPauseOpenAIAccountByQuota(context.Background(), account)
				require.False(t, paused, "the removed 5h window must no longer pause this account")
				require.InDelta(t, 0.78, openAIQuotaHeadroomFactor(account, now), 1e-9)

				account.Extra["codex_7d_used_percent"] = 100.0
				paused, decision = shouldAutoPauseOpenAIAccountByQuota(context.Background(), account)
				require.True(t, paused, "weekly auto-pause must remain active")
				require.Equal(t, "7d", decision.window)

				// Upstream may restore the short window later. Its real quota
				// must take effect again without any permanent exemption flag.
				headers := http.Header{}
				headers.Set("x-codex-primary-used-percent", "22")
				headers.Set("x-codex-primary-window-minutes", "10080")
				headers.Set("x-codex-primary-reset-after-seconds", "345600")
				headers.Set("x-codex-secondary-used-percent", "100")
				headers.Set("x-codex-secondary-window-minutes", "300")
				headers.Set("x-codex-secondary-reset-after-seconds", "3600")
				mergeAccountExtra(account, buildCodexUsageExtraUpdates(ParseCodexRateLimitHeaders(headers), now))
				applyExtraToUsage(usage, account.Extra, now)
				require.NotNil(t, usage.FiveHour)
				require.Equal(t, 100.0, usage.FiveHour.Utilization)
				paused, decision = shouldAutoPauseOpenAIAccountByQuota(context.Background(), account)
				require.True(t, paused)
				require.Equal(t, "5h", decision.window)
			})
		}
	}
}

func TestCodexSnapshotWithoutWindowLayoutPreservesPreviousFiveHour(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	account := &Account{Extra: map[string]any{
		"codex_5h_used_percent":   42.0,
		"codex_5h_window_minutes": 300,
		"codex_5h_reset_at":       now.Add(time.Hour).Format(time.RFC3339Nano),
	}}
	headers := http.Header{}
	headers.Set("x-codex-primary-used-percent", "22")
	mergeAccountExtra(account, buildCodexUsageExtraUpdates(ParseCodexRateLimitHeaders(headers), now))
	usage := &UsageInfo{}
	applyExtraToUsage(usage, account.Extra, now)
	require.NotNil(t, usage.FiveHour)
	require.Equal(t, 42.0, usage.FiveHour.Utilization)
	require.False(t, hasOpenAIWeeklyOnlySnapshot(account.Extra))
}
