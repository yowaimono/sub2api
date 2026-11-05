package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const (
	OpenAIDailyWeeklyBudgetLimitKey = "daily_weekly_budget_percent"
	OpenAIDailyWeeklyBudgetStateKey = "codex_daily_weekly_budget"
	OpenAIWeeklyBudgetCycleHoursKey = "weekly_budget_cycle_hours"
	OpenAIWeeklyBudgetResetHourKey  = "weekly_budget_reset_hour"
)

// Calendar days deliberately use a fixed UTC+8 zone, independent of the host.
var openAIDailyBudgetLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

// OpenAIDailyWeeklyBudgetState is server-managed, persisted with usage snapshots.
// UsedPercent is the calendar-day sum of positive deltas in WEEKLY percentage
// points, not USD or a percentage of remaining allowance. HourlyUsage supplies
// the independently configured enforcement cycle.
type OpenAIDailyWeeklyBudgetState struct {
	Day             string    `json:"day"`
	UsedPercent     float64   `json:"used_percent"`
	LastUsedPercent float64   `json:"last_used_percent"`
	WeeklyResetAt   time.Time `json:"weekly_reset_at"`
	ObservedAt      time.Time `json:"observed_at"`
	// Version 1 retains hourly increments for flexible, hour-aligned cycles.
	HistoryVersion int                        `json:"history_version,omitempty"`
	HourlyUsage    []OpenAIWeeklyBudgetBucket `json:"hourly_usage,omitempty"`
}

type OpenAIWeeklyBudgetBucket struct {
	Hour        time.Time `json:"hour"`
	UsedPercent float64   `json:"used_percent"`
}

type OpenAIWeeklyBudgetCycle struct {
	Hours     int
	ResetHour int
}

// Cycles are fixed (not rolling), anchored to 1970-01-01 at the chosen UTC+8
// hour. This keeps multi-day and non-divisor periods deterministic on all nodes.
func (c OpenAIWeeklyBudgetCycle) Window(now time.Time) (time.Time, time.Time) {
	anchor := time.Date(1970, 1, 1, c.ResetHour, 0, 0, 0, openAIDailyBudgetLocation)
	width := time.Duration(c.Hours) * time.Hour
	elapsed := now.Sub(anchor)
	index := elapsed / width
	if elapsed < 0 && elapsed%width != 0 {
		index--
	}
	start := anchor.Add(index * width)
	return start, start.Add(width)
}

func ResolveOpenAIWeeklyBudgetCycle(ctx context.Context, account *Account) OpenAIWeeklyBudgetCycle {
	settings := openAIQuotaAutoPauseSettingsFromContext(ctx)
	cycle := OpenAIWeeklyBudgetCycle{Hours: settings.WeeklyBudgetCycleHours, ResetHour: settings.WeeklyBudgetResetHour}
	if cycle.Hours < 1 || cycle.Hours > 168 {
		cycle.Hours = 24
	}
	if cycle.ResetHour < 0 || cycle.ResetHour > 23 {
		cycle.ResetHour = 0
	}
	if account != nil {
		if value, ok := resolveAccountExtraNumber(account.Extra, OpenAIWeeklyBudgetCycleHoursKey); ok && validBudgetInteger(value, 1, 168) {
			cycle.Hours = int(value)
		}
		if value, ok := resolveAccountExtraNumber(account.Extra, OpenAIWeeklyBudgetResetHourKey); ok && validBudgetInteger(value, 0, 23) {
			cycle.ResetHour = int(value)
		}
	}
	return cycle
}

func validBudgetInteger(value float64, min, max int) bool {
	return finiteNonnegative(value) && value == math.Trunc(value) && value >= float64(min) && value <= float64(max)
}

// Legacy daily aggregates have no intra-day distribution. Seed them into their
// latest observation hour conservatively; changing policy must not erase them.
func seedOpenAIWeeklyBudgetHistory(state *OpenAIDailyWeeklyBudgetState) {
	if state.HistoryVersion >= 1 {
		return
	}
	if state.UsedPercent > 0 {
		state.HourlyUsage = []OpenAIWeeklyBudgetBucket{{Hour: state.ObservedAt.UTC().Truncate(time.Hour), UsedPercent: state.UsedPercent}}
	}
	state.HistoryVersion = 1
}

func appendOpenAIWeeklyBudgetDelta(state *OpenAIDailyWeeklyBudgetState, observed time.Time, delta float64) {
	hour := observed.UTC().Truncate(time.Hour)
	cutoff := hour.Add(-168 * time.Hour)
	kept := make([]OpenAIWeeklyBudgetBucket, 0, len(state.HourlyUsage)+1)
	for _, bucket := range state.HourlyUsage {
		if !bucket.Hour.Before(cutoff) && !bucket.Hour.After(hour) {
			kept = append(kept, bucket)
		}
	}
	if delta > 0 {
		if len(kept) > 0 && kept[len(kept)-1].Hour.Equal(hour) {
			kept[len(kept)-1].UsedPercent = math.Round((kept[len(kept)-1].UsedPercent+delta)*1e6) / 1e6
		} else {
			kept = append(kept, OpenAIWeeklyBudgetBucket{Hour: hour, UsedPercent: delta})
		}
	}
	state.HourlyUsage = kept
}

func ReadOpenAIDailyWeeklyBudgetState(extra map[string]any) (OpenAIDailyWeeklyBudgetState, bool) {
	var state OpenAIDailyWeeklyBudgetState
	raw, ok := extra[OpenAIDailyWeeklyBudgetStateKey]
	if !ok {
		return state, false
	}
	data, err := json.Marshal(raw)
	if err != nil || json.Unmarshal(data, &state) != nil || state.Day == "" || state.ObservedAt.IsZero() ||
		!validWeeklyPercent(state.LastUsedPercent) || !finiteNonnegative(state.UsedPercent) {
		return OpenAIDailyWeeklyBudgetState{}, false
	}
	return state, true
}

func finiteNonnegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}

func validWeeklyPercent(value float64) bool {
	return finiteNonnegative(value) && value <= 100
}

// FoldOpenAIDailyWeeklyBudget must run under the account row lock. Older or
// duplicate observations cannot replace a newer snapshot or credit back usage.
// A weekly reset does NOT refill today's budget. Small downward corrections
// retain the high-water mark so a later recovery is not counted twice.
func FoldOpenAIDailyWeeklyBudget(extra, updates map[string]any) (OpenAIDailyWeeklyBudgetState, bool) {
	used, ok := resolveAccountExtraNumber(updates, "codex_7d_used_percent")
	if !ok || !validWeeklyPercent(used) {
		return OpenAIDailyWeeklyBudgetState{}, false
	}
	observedAt, err := time.Parse(time.RFC3339Nano, stringValue(updates["codex_usage_updated_at"]))
	if err != nil {
		return OpenAIDailyWeeklyBudgetState{}, false
	}
	resetAt, _ := time.Parse(time.RFC3339Nano, stringValue(updates["codex_7d_reset_at"]))
	state, hasState := ReadOpenAIDailyWeeklyBudgetState(extra)
	if hasState && !observedAt.After(state.ObservedAt) {
		return state, false
	}
	day := observedAt.In(openAIDailyBudgetLocation).Format("2006-01-02")
	if !hasState {
		// Existing snapshots supply the initial baseline when possible. Otherwise
		// this observation establishes it; earlier consumption is unknowable.
		previousAt, _ := time.Parse(time.RFC3339Nano, stringValue(extra["codex_usage_updated_at"]))
		previousUsed, hasPrevious := resolveAccountExtraNumber(extra, "codex_7d_used_percent")
		if hasPrevious && validWeeklyPercent(previousUsed) && !previousAt.IsZero() && previousAt.Before(observedAt) {
			previousReset, _ := time.Parse(time.RFC3339Nano, stringValue(extra["codex_7d_reset_at"]))
			state = OpenAIDailyWeeklyBudgetState{Day: day, LastUsedPercent: previousUsed, WeeklyResetAt: previousReset, ObservedAt: previousAt}
		} else {
			return OpenAIDailyWeeklyBudgetState{Day: day, LastUsedPercent: used, WeeklyResetAt: resetAt, ObservedAt: observedAt, HistoryVersion: 1}, true
		}
	}
	seedOpenAIWeeklyBudgetHistory(&state)
	if state.Day != day {
		state.Day = day
		state.UsedPercent = 0
	}
	// Reset timestamps derived from response headers can drift by seconds.
	// An elapsed timestamp alone is insufficient: upstream may still return
	// the previous window while its reset is propagating. Require a new window
	// timestamp, or a drop after the previous reset. An early reset needs both.
	resetElapsed := !state.WeeklyResetAt.IsZero() && !observedAt.Before(state.WeeklyResetAt)
	resetAdvanced := !state.WeeklyResetAt.IsZero() && !resetAt.IsZero() && resetAt.After(state.WeeklyResetAt.Add(5*time.Minute))
	reset := (resetElapsed && (resetAdvanced || used < state.LastUsedPercent)) || (resetAdvanced && used < state.LastUsedPercent)
	delta := math.Max(0, used-state.LastUsedPercent)
	if reset {
		delta = used
		state.LastUsedPercent = used
	} else if used > state.LastUsedPercent {
		state.LastUsedPercent = used
	}
	state.UsedPercent = math.Round((state.UsedPercent+delta)*1e6) / 1e6
	appendOpenAIWeeklyBudgetDelta(&state, observedAt, delta)
	state.ObservedAt = observedAt
	if reset && !resetAdvanced {
		// Do not reuse an already consumed reset timestamp if upstream has not
		// supplied the next one yet.
		state.WeeklyResetAt = time.Time{}
	} else if resetAt.After(observedAt) {
		state.WeeklyResetAt = resetAt
	} else if reset {
		state.WeeklyResetAt = time.Time{}
	}
	return state, true
}

func ResolveOpenAIDailyWeeklyBudgetLimit(ctx context.Context, account *Account) float64 {
	if account == nil || !account.IsOpenAIOAuth() || account.IsShadow() {
		return 0
	}
	// Missing/null inherits; explicit zero exempts this account.
	if value, ok := resolveAccountExtraNumber(account.Extra, OpenAIDailyWeeklyBudgetLimitKey); ok && validWeeklyPercent(value) {
		return value
	}
	value := openAIQuotaAutoPauseSettingsFromContext(ctx).DailyWeeklyBudgetPercent
	if !validWeeklyPercent(value) {
		return 0
	}
	return value
}

func openAIDailyWeeklyBudgetUsage(ctx context.Context, account *Account, now time.Time) float64 {
	state, ok := ReadOpenAIDailyWeeklyBudgetState(account.Extra)
	if !ok {
		return 0
	}
	seedOpenAIWeeklyBudgetHistory(&state)
	start, end := ResolveOpenAIWeeklyBudgetCycle(ctx, account).Window(now)
	used := 0.0
	for _, bucket := range state.HourlyUsage {
		if !bucket.Hour.Before(start) && bucket.Hour.Before(end) {
			used += bucket.UsedPercent
		}
	}
	return used
}

func shouldPauseOpenAIDailyWeeklyBudget(ctx context.Context, account *Account, now time.Time) (bool, openAIQuotaAutoPauseDecision) {
	limit := ResolveOpenAIDailyWeeklyBudgetLimit(ctx, account)
	if limit <= 0 {
		return false, openAIQuotaAutoPauseDecision{}
	}
	used := openAIDailyWeeklyBudgetUsage(ctx, account, now)
	if used+1e-6 < limit {
		return false, openAIQuotaAutoPauseDecision{}
	}
	return true, openAIQuotaAutoPauseDecision{window: "daily_weekly_budget", threshold: limit / 100, utilization: used / 100, reason: "daily_weekly_budget_exhausted"}
}

// Reorder only the positions occupied by opted-in accounts of the same priority
// and compact capability tier. Exempt accounts retain their existing positions.
// This also avoids a non-transitive sort comparator for mixed configured pools.
func prioritizeOpenAIDailyBudgetAccounts(ctx context.Context, accounts []*Account, requireCompact bool) {
	type groupKey struct{ priority, tier int }
	groups := make(map[groupKey][]int)
	for i, account := range accounts {
		if ResolveOpenAIDailyWeeklyBudgetLimit(ctx, account) <= 0 {
			continue
		}
		key := groupKey{priority: account.Priority}
		if requireCompact {
			key.tier = openAICompactSupportTier(account)
		}
		groups[key] = append(groups[key], i)
	}
	now := time.Now()
	for _, indices := range groups {
		if len(indices) < 2 {
			continue
		}
		pool := make([]*Account, len(indices))
		// Decode retained history once per account, not once per comparison.
		fractions := make(map[*Account]float64, len(indices))
		for i, index := range indices {
			pool[i] = accounts[index]
			fractions[pool[i]] = openAIDailyWeeklyBudgetUsage(ctx, pool[i], now) / ResolveOpenAIDailyWeeklyBudgetLimit(ctx, pool[i])
		}
		sort.SliceStable(pool, func(i, j int) bool { return fractions[pool[i]] < fractions[pool[j]] })
		for i, index := range indices {
			accounts[index] = pool[i]
		}
	}
}

func ValidateOpenAIDailyWeeklyBudgetExtra(extra map[string]any) error {
	for _, field := range []struct {
		key string
		min int
		max int
	}{{OpenAIWeeklyBudgetCycleHoursKey, 1, 168}, {OpenAIWeeklyBudgetResetHourKey, 0, 23}} {
		if raw, exists := extra[field.key]; exists && raw != nil {
			value, ok := resolveAccountExtraNumber(extra, field.key)
			if !ok || !validBudgetInteger(value, field.min, field.max) {
				return fmt.Errorf("%s must be an integer between %d and %d; null inherits the global default", field.key, field.min, field.max)
			}
		}
	}
	if raw, exists := extra[OpenAIDailyWeeklyBudgetLimitKey]; exists && raw != nil {
		value, ok := resolveAccountExtraNumber(extra, OpenAIDailyWeeklyBudgetLimitKey)
		if !ok || !validWeeklyPercent(value) {
			return fmt.Errorf("%s must be a finite number between 0 and 100; null inherits the global default", OpenAIDailyWeeklyBudgetLimitKey)
		}
	}
	return nil
}

// A token rotation is not an account change. Known upstream identities are.
func OpenAIDailyBudgetIdentityChanged(previous, next map[string]any) bool {
	for _, key := range []string{"chatgpt_account_id", "email", "workspace_id"} {
		a, b := firstStringValue(previous, key), firstStringValue(next, key)
		if a != "" && b != "" && !strings.EqualFold(a, b) {
			return true
		}
	}
	return false
}
