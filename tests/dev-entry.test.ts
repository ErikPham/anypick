import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';

const source = readFileSync(join(process.cwd(), 'scripts/dev.mjs'), 'utf8');

describe('development entry', () => {
  it('starts the native macOS app when pnpm dev has no CLI command', () => {
    expect(source).toContain("process.platform === 'darwin' && !hasCommandOrOption");
    expect(source).toContain("args.push('--tray')");
    expect(source).toContain('const watch = shouldWatch(args)');
  });

  it('preserves explicit CLI options and the watch overrides', () => {
    expect(source).toContain("arg !== '--watch' && arg !== '--no-watch'");
    expect(source).toContain("argv.includes('--watch')");
    expect(source).toContain("argv.includes('--no-watch')");
  });
});
