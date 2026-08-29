import type { LiveUsage } from '../types';

function record(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined;
}

function cent(value: unknown): number | undefined {
  const val = record(value)?.val;
  return typeof val === 'number' && Number.isFinite(val) ? val : undefined;
}

export function parseGrokUsage(value: unknown): LiveUsage | null {
  const config = record(record(value)?.config);
  if (!config) {
    return null;
  }

  const period = record(config.currentPeriod);
  const periodType = typeof period?.type === 'string' ? period.type : '';
  const label = periodType.includes('WEEKLY')
    ? 'Weekly'
    : periodType.includes('MONTHLY')
      ? 'Monthly'
      : 'Current period';
  const directPercent = config.creditUsagePercent;
  const limit = cent(config.monthlyLimit);
  const used = cent(config.used);
  const usedPercent =
    typeof directPercent === 'number' && Number.isFinite(directPercent)
      ? directPercent
      : limit != null && limit > 0 && used != null
        ? (used / limit) * 100
        : undefined;
  if (usedPercent == null) {
    return null;
  }

  const rawReset =
    typeof period?.end === 'string'
      ? period.end
      : typeof config.billingPeriodEnd === 'string'
        ? config.billingPeriodEnd
        : undefined;
  const reset = rawReset ? Date.parse(rawReset) : Number.NaN;
  return {
    windows: [
      {
        label,
        remainingPercent: Math.round(Math.max(0, Math.min(100, 100 - usedPercent))),
        ...(Number.isFinite(reset) ? { resetsAtMs: reset } : {}),
      },
    ],
  };
}
