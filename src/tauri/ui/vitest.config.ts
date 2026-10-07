import { defineConfig } from 'vitest/config';
import { resolve } from 'node:path';
import react from '@vitejs/plugin-react';

import { appVersionDefine } from './scripts/app-version';

export default defineConfig({
  plugins: [react()],
  define: appVersionDefine(resolve(import.meta.dirname, '../../..')),
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./test/setup.ts'],
    include: ['test/**/*.test.{ts,tsx}'],
    restoreMocks: true,
  },
});
