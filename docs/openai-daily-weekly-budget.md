# OpenAI OAuth periodic weekly-allowance budgets

This optional budget distributes weekly subscription capacity over configurable fixed cycles.
It applies to regular OpenAI OAuth accounts. It does not apply to API keys, other
providers, or Spark shadow accounts with a separate upstream quota dimension.

## Configure

In **Operations (`/admin/ops`) → Settings → Advanced settings → OpenAI quota
auto-pause**, set the OpenAI OAuth weekly-allowance budget default to e.g. **10**.
The default is **0** (disabled). In **Accounts (`/admin/accounts`) → Edit →
Periodic weekly-allowance budget**, each eligible account can inherit the default,
set a custom value, or opt out.
Values are weekly **percentage points**, between 0 and 100. They are neither USD
nor a percentage of the remaining weekly allowance.

Example: with a daily budget of 10 and weekly usage initially at 25%, the account
stops accepting new requests once observed weekly usage reaches 35%. The account
automatically becomes eligible again at **00:00 Asia/Shanghai (UTC+8)** by default, subject
to all other scheduling gates. The service does not modify `schedulable`.

The API stores the global setting as
`openai_account_quota_auto_pause.daily_weekly_budget_percent` inside existing
advanced operations settings. Per-account `extra.daily_weekly_budget_percent`
uses missing/null to inherit, a positive number to override, and explicit 0 to
opt out. Saving other settings does not reset consumption.

### Flexible budget reset cycles

Both pages also expose **Budget reset interval (hours)** (1–168 whole hours) and
**Cycle anchor hour (UTC+8)** (00:00–23:00). The defaults are **24 hours / 00:00**.
Examples: **6 / 00:00** resets at 00:00, 06:00, 12:00, 18:00;
**24 / 08:00** resets daily at 08:00; **48 / 08:00** resets every two days.
These are fixed, non-rolling windows aligned to the selected hour on
1970-01-01 UTC+8; this also defines the phase for intervals that do not divide
24 and multi-day cycles. The percentage allowance applies once per cycle and is
not automatically scaled when the cycle length changes.

Global settings use `weekly_budget_cycle_hours` and `weekly_budget_reset_hour`
inside `openai_account_quota_auto_pause`. Missing/zero global cycle hours retain
the legacy 24-hour default. Per-account `extra` uses the same keys, with absent/null
inheriting each global field independently. An explicit reset hour of **0** means
midnight, not inheritance. The account interval can be left blank to inherit.

Usage history is retained in at most 169 hourly buckets. Changing policy queries
that retained history rather than resetting counters. There is no history before
the feature begins observing an account. Legacy daily aggregates without hourly
detail are conservatively assigned to their latest observation hour on upgrade;
short custom cycles may therefore initially overcount historical consumption.
The account usage label continues to show the **UTC+8 calendar-day total**,
independently of the selected enforcement cycle.

This budget reset is distinct from **Accounts → Auto refresh** (5/10/15/30 seconds),
which reloads displayed data. Upstream usage observations arrive from responses,
probes and rate-limit handling; this setting does not introduce a polling task.

## Accounting and routing

The server persists a ledger in `extra.codex_daily_weekly_budget`. Weekly usage
observations from response headers, active probes, and rate-limit handling are
folded under the same PostgreSQL account row lock as the canonical snapshot.
Concurrent callbacks serialize; duplicate or older observations cannot rewind
the ledger. The ledger survives process restarts and ordinary account edits.
Managed state cannot be replaced by account create/edit/bulk-edit payloads.

The first usable weekly observation establishes a baseline; an existing previous
snapshot supplies a baseline when available. Earlier unobserved consumption
cannot be reconstructed. A downward correction preserves the weekly high-water
mark, rather than granting credit or counting its recovery twice. A new official
weekly window counts its new usage without clearing consumption already recorded
that day. Small reset-time drift does not create another weekly window. A large
reset-time advance together with lower usage also recognizes an early reset.
An increment first observed after a cycle boundary is conservatively attributed
to the new cycle, even if some of its work ran before the boundary. Calendar-day
display statistics independently attribute it to the observation's UTC+8 day.

Both legacy and advanced schedulers filter exhausted accounts before selection
and during subsequent account rechecks. Sticky affinity also respects the cap;
an active session stays on its account until an existing eligibility gate or the
cycle cap rejects it. For new sessions, opted-in accounts at the same explicit
priority and compact-capability tier prefer the smallest consumed fraction of
their cycle budget. Exempt accounts retain their existing ranking positions.
Advanced scheduling considers the full eligible pool for budget balancing so a
small score-based TopK cannot hide accounts with unused daily capacity. If every
eligible account has exhausted its budget, the normal no-available-account error
is returned; caps are not silently bypassed.

## Practical limits

This is an **observed-use limit**, not an upstream reservation. Requests already
running and delayed, rounded, or missing upstream usage reports can cause
overshoot. Enabled accounts bypass the normal display-snapshot write throttle
for weekly observations, but the service cannot know the consumption of an
unfinished request. An account without usable weekly reports cannot have its
percentage consumption inferred from API dollar pricing. This feature does not
guarantee avoidance of upstream rate limits or account restrictions.

No database migration or new infrastructure is required. Deploy the application
and set the desired global default or individual overrides. To disable the
feature, set the global default to 0 and remove/disable positive account overrides.
Existing 5h/7d cumulative thresholds and other eligibility checks still apply.

## Persistence regression test

From `backend`, run `go test -tags integration ./internal/repository -run
TestDailyWeeklyBudgetConcurrentPersistenceAndStaleAdminEdit -count=1`. The harness
starts isolated PostgreSQL and Redis containers and applies migrations. Optional
`SUB2API_TEST_POSTGRES_IMAGE` and `SUB2API_TEST_REDIS_IMAGE` environment variables
allow locally available test images. On Docker Desktop, set
`TESTCONTAINERS_HOST_OVERRIDE=127.0.0.1` if the IPv6 localhost forwarding fails.
