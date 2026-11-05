import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import OpenAIDailyBudgetSettings from '../OpenAIDailyBudgetSettings.vue'
import OpenAIDailyBudgetUsage from '../OpenAIDailyBudgetUsage.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string, params?: { percent?: string }) => params?.percent ? `${key}: ${params.percent}` : key })
}))

afterEach(() => vi.useRealTimers())

describe('daily weekly budget controls', () => {
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
