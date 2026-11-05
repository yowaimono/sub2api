package repository

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type dailyWeeklyBudgetUpdateKey struct{}

// Serialize the ledger fold and the upstream snapshot in the SAME transaction.
// All usage writers already pass through UpdateExtra, including probes, 429s
// and the asynchronous Responses-header path. No instance-local counter is used.
func (r *accountRepository) updateDailyWeeklyBudgetSnapshot(ctx context.Context, id int64, updates map[string]any) error {
	baseCtx := ctx
	var tx *dbent.Tx
	if dbent.TxFromContext(ctx) == nil {
		var err error
		tx, err = r.client.Tx(ctx)
		if err != nil && !errors.Is(err, dbent.ErrTxStarted) {
			return err
		}
		if tx != nil {
			defer func() { _ = tx.Rollback() }()
			ctx = dbent.NewTxContext(ctx, tx)
		}
	}
	client := clientFromContext(ctx, r.client)
	rows, err := client.QueryContext(ctx, `SELECT platform, type, COALESCE(extra, '{}'::jsonb), quota_dimension
		FROM accounts WHERE id = $1 AND deleted_at IS NULL FOR NO KEY UPDATE`, id)
	if err != nil {
		return err
	}
	var platform, accountType, dimension string
	var raw []byte
	if !rows.Next() {
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return err
		}
		return service.ErrAccountNotFound
	}
	err = rows.Scan(&platform, &accountType, &raw, &dimension)
	_ = rows.Close()
	if err != nil {
		return err
	}
	var extra map[string]any
	if err := json.Unmarshal(raw, &extra); err != nil {
		return err
	}
	updates = maps.Clone(updates)
	if platform == service.PlatformOpenAI && accountType == service.AccountTypeOAuth && dimension != "spark" {
		previous, _ := time.Parse(time.RFC3339Nano, stringFromExtra(extra, "codex_usage_updated_at"))
		observed, err := time.Parse(time.RFC3339Nano, stringFromExtra(updates, "codex_usage_updated_at"))
		if err == nil && !previous.IsZero() && !observed.After(previous) {
			// A delayed callback must not roll the canonical snapshot backwards.
			for key := range updates {
				if isDailyBudgetSnapshotKey(key) {
					delete(updates, key)
				}
			}
		} else if state, changed := service.FoldOpenAIDailyWeeklyBudget(extra, updates); changed {
			updates[service.OpenAIDailyWeeklyBudgetStateKey] = state
		}
	}
	if len(updates) > 0 {
		if err := r.UpdateExtra(context.WithValue(ctx, dailyWeeklyBudgetUpdateKey{}, true), id, updates); err != nil {
			return err
		}
	}
	if tx != nil {
		if err := tx.Commit(); err != nil {
			return err
		}
		r.syncSchedulerAccountSnapshot(baseCtx, id)
	}
	return nil
}

func stringFromExtra(extra map[string]any, key string) string {
	value, _ := extra[key].(string)
	return value
}

func isDailyBudgetSnapshotKey(key string) bool {
	return key == service.OpenAIDailyWeeklyBudgetStateKey || key == "codex_usage_updated_at" ||
		strings.HasPrefix(key, "codex_7d_") || strings.HasPrefix(key, "codex_5h_") ||
		strings.HasPrefix(key, "codex_primary_") || strings.HasPrefix(key, "codex_secondary_")
}

// Ordinary edits use full Extra replacement. Preserve runtime data from the
// locked row, not a stale form or the earlier service-level GetByID result.
func preserveDailyWeeklyBudgetSnapshot(ctx context.Context, client *dbent.Client, account *service.Account, extra map[string]any) error {
	delete(extra, service.OpenAIDailyWeeklyBudgetStateKey)
	if !account.IsOpenAIOAuth() || account.IsShadow() {
		return nil
	}
	rows, err := client.QueryContext(ctx, `SELECT COALESCE(extra, '{}'::jsonb), credentials
		FROM accounts WHERE id = $1 AND deleted_at IS NULL FOR NO KEY UPDATE`, account.ID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return err
		}
		return service.ErrAccountNotFound
	}
	var rawExtra, rawCredentials []byte
	if err := rows.Scan(&rawExtra, &rawCredentials); err != nil {
		return err
	}
	var current, credentials map[string]any
	if err := json.Unmarshal(rawExtra, &current); err != nil {
		return err
	}
	if err := json.Unmarshal(rawCredentials, &credentials); err != nil {
		return err
	}
	for key := range extra {
		if isDailyBudgetSnapshotKey(key) {
			delete(extra, key)
		}
	}
	if !service.OpenAIDailyBudgetIdentityChanged(credentials, account.Credentials) {
		for key, value := range current {
			if isDailyBudgetSnapshotKey(key) {
				extra[key] = value
			}
		}
	}
	return rows.Err()
}
