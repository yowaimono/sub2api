<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useNow } from '@vueuse/core'

const props = withDefaults(defineProps<{
  cycleHours?: number | null
  resetHour?: number | null
  allowInherit?: boolean
}>(), { cycleHours: null, resetHour: null, allowInherit: true })
const emit = defineEmits<{
  'update:cycleHours': [value: number | null]
  'update:resetHour': [value: number | null]
}>()
const { t } = useI18n()
const now = useNow({ interval: 30_000 })
const hours = computed(() => props.cycleHours ?? '')
const nextReset = computed(() => {
  const duration = props.cycleHours
  const hour = props.resetHour
  if (duration == null || hour == null || !Number.isInteger(duration) || duration < 1 || duration > 168 ||
      !Number.isInteger(hour) || hour < 0 || hour > 23) return null
  const width = duration * 3600_000
  const anchor = (hour - 8) * 3600_000
  const next = anchor + (Math.floor((now.value.getTime() - anchor) / width) + 1) * width
  // An explicit UTC+8 label is consistent regardless of the browser timezone.
  return new Date(next + 8 * 3600_000).toISOString().slice(0, 16).replace('T', ' ')
})
function changeHours(event: Event) {
  const input = event.target as HTMLInputElement
  emit('update:cycleHours', input.value === '' && props.allowInherit ? null : input.valueAsNumber)
}
function changeResetHour(event: Event) {
  const input = event.target as HTMLSelectElement
  emit('update:resetHour', input.value === '' ? null : Number(input.value))
}
</script>

<template>
  <div class="space-y-2" data-testid="weekly-budget-cycle-settings">
    <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
      <div>
        <label class="input-label">{{ t('admin.accounts.dailyWeeklyBudget.cycleHours') }}</label>
        <input
          :value="hours" type="number" min="1" max="168" step="1" class="input"
          :placeholder="allowInherit ? t('admin.accounts.dailyWeeklyBudget.inherit') : '24'"
          data-testid="weekly-budget-cycle-hours" @input="changeHours"
        />
      </div>
      <div>
        <label class="input-label">{{ t('admin.accounts.dailyWeeklyBudget.resetHour') }}</label>
        <select class="input" :value="resetHour ?? ''" data-testid="weekly-budget-reset-hour" @change="changeResetHour">
          <option v-if="allowInherit" value="">{{ t('admin.accounts.dailyWeeklyBudget.inherit') }}</option>
          <option v-for="hour in 24" :key="hour - 1" :value="hour - 1">{{ String(hour - 1).padStart(2, '0') }}:00</option>
        </select>
      </div>
    </div>
    <p class="input-hint">{{ t('admin.accounts.dailyWeeklyBudget.cycleHint') }}</p>
    <p v-if="nextReset" class="input-hint" data-testid="weekly-budget-next-reset">
      {{ t('admin.accounts.dailyWeeklyBudget.nextReset', { time: nextReset }) }}
    </p>
    <p v-if="allowInherit" class="input-hint">{{ t('admin.accounts.dailyWeeklyBudget.cycleInheritHint') }}</p>
  </div>
</template>
