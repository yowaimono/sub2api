<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import OpenAIWeeklyBudgetCycleSettings from './OpenAIWeeklyBudgetCycleSettings.vue'

const props = defineProps<{ modelValue: number | null; cycleHours?: number | null; resetHour?: number | null }>()
const emit = defineEmits<{
  'update:modelValue': [value: number | null]
  'update:cycleHours': [value: number | null]
  'update:resetHour': [value: number | null]
}>()
const { t } = useI18n()
const mode = computed(() => props.modelValue == null ? 'inherit' : props.modelValue === 0 ? 'disabled' : 'custom')
function changeMode(event: Event) {
  const value = (event.target as HTMLSelectElement).value
  emit('update:modelValue', value === 'inherit' ? null : value === 'disabled' ? 0 : 10)
}
function changeLimit(event: Event) {
  const input = event.target as HTMLInputElement
  emit('update:modelValue', input.valueAsNumber)
}
</script>

<template>
  <div class="space-y-3 border-t border-gray-200 pt-4 dark:border-dark-600" data-testid="daily-weekly-budget-settings">
    <label class="input-label">{{ t('admin.accounts.dailyWeeklyBudget.title') }}</label>
    <select class="input" :value="mode" data-testid="daily-weekly-budget-mode" @change="changeMode">
      <option value="inherit">{{ t('admin.accounts.dailyWeeklyBudget.inherit') }}</option>
      <option value="custom">{{ t('admin.accounts.dailyWeeklyBudget.custom') }}</option>
      <option value="disabled">{{ t('admin.accounts.dailyWeeklyBudget.disabled') }}</option>
    </select>
    <input
      v-if="mode === 'custom'"
      :value="modelValue"
      type="number"
      min="0.1"
      max="100"
      step="0.1"
      class="input"
      data-testid="daily-weekly-budget-limit"
      @input="changeLimit"
    />
    <p class="input-hint">{{ t('admin.accounts.dailyWeeklyBudget.hint') }}</p>
    <OpenAIWeeklyBudgetCycleSettings
      v-if="mode !== 'disabled'" :cycle-hours="cycleHours" :reset-hour="resetHour"
      @update:cycle-hours="emit('update:cycleHours', $event)"
      @update:reset-hour="emit('update:resetHour', $event)"
    />
  </div>
</template>
