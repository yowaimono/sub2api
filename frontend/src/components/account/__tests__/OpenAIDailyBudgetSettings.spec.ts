import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import OpenAIDailyBudgetSettings from '../OpenAIDailyBudgetSettings.vue'
import OpenAIDailyBudgetUsage from '../OpenAIDailyBudgetUsage.vue'
import OpenAIWeeklyBudgetCycleSettings from '../OpenAIWeeklyBudgetCycleSettings.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string, params?: { percent?: string; time?: string }) => `${key}${params?.percent ? `: ${params.percent}` : params?.time ? `: ${params.time}` : ''}` })
}))

afterEach(() => vi.useRealTimers())

describe('daily weekly budget controls', () => {
  it('previews the next UTC+8 reset and refreshes when the boundary passes', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-11-04T23:59:45Z'))
    const wrapper = mount(OpenAIWeeklyBudgetCycleSettings, { props: { cycleHours: 24, resetHour: 8, allowInherit: false } })
    expect(wrapper.get('[data-testid="weekly-budget-next-reset"]').text()).toContain('2026-11-05 08:00')
    vi.advanceTimersByTime(30_000)
    await nextTick()
    expect(wrapper.get('[data-testid="weekly-budget-next-reset"]').text()).toContain('2026-11-06 08:00')
    await wrapper.setProps({ cycleHours: null, resetHour: null, allowInherit: true })
    expect(wrapper.find('[data-testid="weekly-budget-next-reset"]').exists()).toBe(false)
    wrapper.unmount()
  })

  it('supports custom whole-hour cycles and independently inherited anchor hours', async () => {
    const wrapper = mount(OpenAIWeeklyBudgetCycleSettings, { props: { cycleHours: null, resetHour: null } })
    const hours = wrapper.get('[data-testid="weekly-budget-cycle-hours"]')
    const anchor = wrapper.get('[data-testid="weekly-budget-reset-hour"]')
    expect((hours.element as HTMLInputElement).value).toBe('')
    await hours.setValue('6')
    expect(wrapper.emitted('update:cycleHours')?.at(-1)).toEqual([6])
    await hours.setValue('')
    expect(wrapper.emitted('update:cycleHours')?.at(-1)).toEqual([null])
    await anchor.setValue('8')
    expect(wrapper.emitted('update:resetHour')?.at(-1)).toEqual([8])
    await anchor.setValue('0')
    expect(wrapper.emitted('update:resetHour')?.at(-1)).toEqual([0])
    await anchor.setValue('')
    expect(wrapper.emitted('update:resetHour')?.at(-1)).toEqual([null])
    wrapper.unmount()
  })

  it('does not silently restore defaults when a required global interval is cleared', async () => {
    const wrapper = mount(OpenAIWeeklyBudgetCycleSettings, { props: { cycleHours: 24, resetHour: 0, allowInherit: false } })
    expect(wrapper.find('option[value=""]').exists()).toBe(false)
    await wrapper.get('input').setValue('')
    expect(wrapper.emitted('update:cycleHours')?.at(-1)?.[0]).toBeNaN()
    wrapper.unmount()
  })

  it('distinguishes inherit, custom, and explicit exemption', async () => {
    const wrapper = mount(OpenAIDailyBudgetSettings, { props: { modelValue: null } })
    expect(wrapper.get('select').element.value).toBe('inherit')
    await wrapper.get('select').setValue('custom')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([10])
    await wrapper.setProps({ modelValue: 10 })
    await wrapper.get('input').setValue('30')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([30])
    await wrapper.get('select').setValue('disabled')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([0])
    await wrapper.setProps({ modelValue: 0 })
    expect(wrapper.find('input').exists()).toBe(false)
    await wrapper.get('select').setValue('inherit')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual([null])
    wrapper.unmount()
  })

  it('displays weekly percentage points and refreshes at UTC+8 midnight', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-11-05T15:59:45Z'))
    const account = {
      platform: 'openai', type: 'oauth', parent_account_id: null,
      extra: { codex_daily_weekly_budget: { day: '2026-11-05', used_percent: 9.5 } }
    } as any
    const wrapper = mount(OpenAIDailyBudgetUsage, { props: { account } })
    expect(wrapper.text()).toContain('9.5')
    vi.advanceTimersByTime(30_000)
    await nextTick()
    expect(wrapper.text()).toContain('0.0')
    wrapper.unmount()
  })
})
