import type { LiveUsage } from '../types';

interface GeminiQuotaBucket {
  modelId?: unknown;
  remainingFraction?: unknown;
  resetTime?: unknown;
}

function modelFamily(model: string): string {
  const lower = model.toLowerCase();
  if (lower.startsWith('gemini')) {
    return 'Gemini';
  }
  if (lower.startsWith('claude')) {
    return 'Claude';
  }
  if (lower.startsWith('gpt')) {
    return 'GPT';
  }
  if (lower.startsWith('chat')) {
    return 'Chat';
  }
  if (lower.startsWith('tab')) {
    return 'Tab';
  }
  return model.split(/[-_]/u)[0] || 'Other';
}

function groupGeminiWindows(
  windows: Array<{ label: string; remainingPercent: number; resetsAtMs?: number }>,
) {
  const groups = new Map<string, typeof windows>();
  for (const window of windows) {
    const key = `${window.remainingPercent}:${window.resetsAtMs ?? 'none'}`;
    groups.set(key, [...(groups.get(key) ?? []), window]);
  }
  return [...groups.values()]
    .map((group) => {
      const first = group[0];
      if (!first || group.length === 1) {
        return first;
      }
      const families = [...new Set(group.map((window) => modelFamily(window.label)))];
      return {
        ...first,
        label: `${families.join(' + ')} (${group.length})`,
      };
    })
    .filter((window): window is NonNullable<typeof window> => Boolean(window));
}

export function parseGeminiUsage(value: unknown): LiveUsage | null {
  if (!value || typeof value !== 'object') {
    return null;
  }
  const buckets = (value as { buckets?: unknown }).buckets;
  if (!Array.isArray(buckets)) {
    return null;
  }
  const windows = buckets.flatMap((raw) => {
    if (!raw || typeof raw !== 'object') {
      return [];
    }
    const bucket = raw as GeminiQuotaBucket;
    if (
      typeof bucket.modelId !== 'string' ||
      typeof bucket.remainingFraction !== 'number' ||
      !Number.isFinite(bucket.remainingFraction)
    ) {
      return [];
    }
    const reset = typeof bucket.resetTime === 'string' ? Date.parse(bucket.resetTime) : Number.NaN;
    return [
      {
        label: bucket.modelId.replace(/^models\//u, ''),
        remainingPercent: Math.round(Math.max(0, Math.min(1, bucket.remainingFraction)) * 100),
        ...(Number.isFinite(reset) ? { resetsAtMs: reset } : {}),
      },
    ];
  });
  return windows.length ? { windows: groupGeminiWindows(windows) } : null;
}
