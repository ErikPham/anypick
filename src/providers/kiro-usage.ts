import type { LiveUsage, LiveUsageWindow } from '../types';

function record(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : undefined;
}

function number(value: unknown): number | undefined {
  if (typeof value === 'number' && Number.isFinite(value)) {
    return value;
  }
  if (typeof value === 'string' && value.trim()) {
    const parsed = Number(value);
    if (Number.isFinite(parsed)) {
      return parsed;
    }
  }
  return undefined;
}

function humanize(value: string): string {
  const words = value.replace(/([a-z])([A-Z])/gu, '$1 $2').replace(/[_-]+/gu, ' ');
  const normalized = words === words.toUpperCase() ? words.toLowerCase() : words;
  return normalized.replace(/\b\w/gu, (char) => char.toUpperCase());
}

export function parseKiroUsage(value: unknown): LiveUsage | null {
  const root = record(value);
  if (!root) {
    return null;
  }
  const rawBreakdown = root.usageBreakdown ?? root.usage_breakdown ?? root.usageBreakdownList;
  if (!Array.isArray(rawBreakdown)) {
    return null;
  }
  const rawReset = root.nextDateReset ?? root.next_date_reset;
  const reset = typeof rawReset === 'string' ? Date.parse(rawReset) : Number.NaN;
  const windows: LiveUsageWindow[] = [];
  for (const raw of rawBreakdown) {
    const item = record(raw);
    if (!item) {
      continue;
    }
    const used = number(item.currentUsage ?? item.current_usage);
    const limit = number(item.usageLimit ?? item.usage_limit ?? item.totalUsageLimit);
    const resource = item.resourceType ?? item.resource_type;
    if (used == null || limit == null || limit <= 0 || typeof resource !== 'string') {
      continue;
    }
    windows.push({
      label: humanize(resource),
      remainingPercent: Math.round(Math.max(0, Math.min(1, (limit - used) / limit)) * 100),
      ...(Number.isFinite(reset) ? { resetsAtMs: reset } : {}),
    });
  }
  return windows.length ? { windows } : null;
}
