import { emitFixtureLine } from './fixture-wire.mts';

// Emits the same synthetic snapshot used by the UI tests. Run with
// `vite-node --config vitest.config.ts` so visual tooling never starts the
// real desktop-service plugin.
process.stdout.write(`${emitFixtureLine(process.argv.includes('--rich'))}\n`);
