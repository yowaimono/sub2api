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
)

// Calendar days deliberately use a fixed UTC+8 zone, independent of the host.
var openAIDailyBudgetLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

// OpenAIDailyWeeklyBudgetState is server-managed, persisted with usage snapshots.
// UsedPercent is a sum of positive deltas in WEEKLY percentage points, not USD
// and not a percentage of the remaining weekly allowance.
type OpenAIDailyWeeklyBudgetState struct {
	Day             string    `json:"day"`
	UsedPercent     float64   `json:"used_percent"`
	LastUsedPercent float64   `json:"last_used_percent"`
	WeeklyResetAt   time.Time `json:"weekly_reset_at"`
	ObservedAt      time.Time `json:"observed_at"`
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
			return OpenAIDailyWeeklyBudgetState{Day: day, LastUsedPercent: used, WeeklyResetAt: resetAt, ObservedAt: observedAt}, true
		}
	}
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

func openAIDailyWeeklyBudgetUsage(account *Account, now time.Time) float64 {
	state, ok := ReadOpenAIDailyWeeklyBudgetState(account.Extra)
	if !ok || state.Day != now.In(openAIDailyBudgetLocation).Format("2006-01-02") {
		return 0
	}
	return state.UsedPercent
}

func shouldPauseOpenAIDailyWeeklyBudget(ctx context.Context, account *Account, now time.Time) (bool, openAIQuotaAutoPauseDecision) {
	limit := ResolveOpenAIDailyWeeklyBudgetLimit(ctx, account)
	if limit <= 0 {
		return false, openAIQuotaAutoPauseDecision{}
	}
	used := openAIDailyWeeklyBudgetUsage(account, now)
	if used+1e-6 < limit {
		return false, openAIQuotaAutoPauseDecision{}
	}
	return true, openAIQuotaAutoPauseDecision{window: "daily_weekly_budget", threshold: limit / 100, utilization: used / 100, reason: "daily_weekly_budget_exhausted"}
}

// Compare only opted-in accounts at the same explicit priority. Existing sticky
// sessions stay bound until a hard budget gate rejects the account.
func compareOpenAIDailyWeeklyBudget(ctx context.Context, a, b *Account, now time.Time) int {
	if a.Priority != b.Priority {
		return 0
	}
	limitA, limitB := ResolveOpenAIDailyWeeklyBudgetLimit(ctx, a), ResolveOpenAIDailyWeeklyBudgetLimit(ctx, b)
	if limitA <= 0 || limitB <= 0 {
		return 0
	}
	usedA, usedB := openAIDailyWeeklyBudgetUsage(a, now)/limitA, openAIDailyWeeklyBudgetUsage(b, now)/limitB
	if math.Abs(usedA-usedB) < 1e-6 {
		return 0
	}
	if usedA < usedB {
		return -1
	}
	return 1
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
		pool := make([]*Account, len(indices))
		for i, index := range indices {
			pool[i] = accounts[index]
		}
		sort.SliceStable(pool, func(i, j int) bool { return compareOpenAIDailyWeeklyBudget(ctx, pool[i], pool[j], now) < 0 })
		for i, index := range indices {
			accounts[index] = pool[i]
		}
	}
}

func ValidateOpenAIDailyWeeklyBudgetExtra(extra map[string]any) error {
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
