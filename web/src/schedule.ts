import type { MirrorSchedule } from './api'

const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat']
export const dayName = (i: number) => DAYS[i] ?? String(i)

export function describeSchedule(s: MirrorSchedule): string {
  if (s.paused) return 'Paused'
  const at = ` at ${s.time_of_day} ${s.timezone}`
  switch (s.frequency) {
    case 'daily': return 'Daily' + at
    case 'weekly': return `Weekly on ${[...s.weekdays].sort((a, b) => a - b).map(dayName).join(', ')}` + at
    case 'monthly': return `Monthly on day ${s.day_of_month}` + at
    default: return 'Manual only'
  }
}
