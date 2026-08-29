import type { LiveUsage, LiveUsageWindow } from '../types';

function record(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined;
}

export function parseOpenCodeUsage(value: unknown): LiveUsage | null {
  const usage = record(record(value)?.usage);
  if (!usage) {
    return null;
  }

  const windows: LiveUsageWindow[] = [];
  for (const [id, label] of [
    ['rolling', '5 hours'],
    ['weekly', 'Weekly'],
    ['monthly', 'Monthly'],
  ] as const) {
    const entry = record(usage[id]);
    const percent = entry?.percent;
    if (typeof percent !== 'number' || !Number.isFinite(percent)) {
      continue;
    }
    const reset = typeof entry?.resetsAt === 'string' ? Date.parse(entry.resetsAt) : Number.NaN;
    windows.push({
      label,
      remainingPercent: Math.round(Math.max(0, Math.min(100, 100 - percent))),
      ...(Number.isFinite(reset) ? { resetsAtMs: reset } : {}),
    });
  }
  return windows.length ? { windows } : null;
}
