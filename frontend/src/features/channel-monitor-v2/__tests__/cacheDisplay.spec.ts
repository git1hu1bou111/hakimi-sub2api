import { describe, expect, it } from 'vitest'
import { cacheRateForDisplay, formatMonitorPercent } from '../monitorFormat'

describe('cache display ceiling', () => {
  it('tracks real changes instead of sticking at the ceiling', () => {
    expect(formatMonitorPercent(cacheRateForDisplay(.943, 90))).toBe('88.1%')
    expect(formatMonitorPercent(cacheRateForDisplay(.967, 90))).toBe('88.9%')
    expect(formatMonitorPercent(cacheRateForDisplay(.99, 90))).toBe('89.7%')
    expect(formatMonitorPercent(cacheRateForDisplay(.928, 90))).toBe('87.6%')
  })
  it('keeps low rates and unconfigured groups unchanged', () => {
    expect(cacheRateForDisplay(.81, 90)).toBe(.81)
    expect(cacheRateForDisplay(.943)).toBe(.943)
    expect(cacheRateForDisplay(.943, null)).toBe(.943)
    expect(cacheRateForDisplay(.943, NaN)).toBe(.943)
    expect(cacheRateForDisplay(.943, 100)).toBeCloseTo(.943)
  })
  it('is continuous, monotonic and never exceeds either the real rate or ceiling', () => {
    for (const cap of [0, 3, 50, 90, 100]) {
      let previous = 0
      for (let i = 0; i <= 1000; i++) {
        const value = i / 1000
        const display = cacheRateForDisplay(value, cap)
        expect(display).toBeGreaterThanOrEqual(previous - 1e-12)
        expect(display).toBeLessThanOrEqual(Math.min(value, cap / 100) + 1e-12)
        previous = display
      }
    }
  })
})
