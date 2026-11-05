<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useNow } from '@vueuse/core'
import type { Account } from '@/types'

const props = defineProps<{ account: Account }>()
const { t } = useI18n()
const now = useNow({ interval: 30_000 })
const state = computed(() => props.account.extra?.codex_daily_weekly_budget as { day?: string; used_percent?: number } | undefined)
const today = () => new Date(now.value.getTime() + 8 * 3600_000).toISOString().slice(0, 10)
const used = computed(() => state.value?.day === today() && typeof state.value.used_percent === 'number' ? state.value.used_percent : 0)
</script>

<template>
  <div
    v-if="account.platform === 'openai' && account.type === 'oauth' && !account.parent_account_id && state"
    class="mb-1 text-xs text-sky-700 dark:text-sky-300"
    :title="t('admin.accounts.dailyWeeklyBudget.usageHint')"
    data-testid="daily-weekly-budget-usage"
  >
    {{ t('admin.accounts.dailyWeeklyBudget.used', { percent: used.toFixed(1) }) }}
  </div>
</template>
