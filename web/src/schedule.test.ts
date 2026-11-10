import { describe, it, expect } from 'vitest'
import { describeSchedule } from './schedule'

const base = { frequency: 'manual' as const, time_of_day: '02:00', timezone: 'America/New_York', weekdays: [], day_of_month: 1, paused: false }

describe('describeSchedule', () => {
  it('manual', () => expect(describeSchedule(base)).toBe('Manual only'))
  it('paused wins', () => expect(describeSchedule({ ...base, frequency: 'daily', paused: true })).toBe('Paused'))
  it('daily', () => expect(describeSchedule({ ...base, frequency: 'daily' })).toBe('Daily at 02:00 America/New_York'))
  it('weekly', () => expect(describeSchedule({ ...base, frequency: 'weekly', weekdays: [4, 2] })).toBe('Weekly on Tue, Thu at 02:00 America/New_York'))
  it('monthly', () => expect(describeSchedule({ ...base, frequency: 'monthly', day_of_month: 15 })).toBe('Monthly on day 15 at 02:00 America/New_York'))
})
