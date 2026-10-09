import type { Plugin } from 'vite';
import { spawn, type ChildProcess } from 'node:child_process';
import { existsSync } from 'node:fs';
import { resolve } from 'node:path';
import type { IncomingMessage, ServerResponse } from 'node:http';
import { readTauriVersion } from './app-version';
import { PROTOCOL_VERSION } from '../lib/wire';

function hostPlatform(nodePlatform: string): string {
  if (nodePlatform === 'darwin') return 'macos';
  if (nodePlatform === 'win32') return 'windows';
  return 'linux';
}

/**
 * No version literals in this payload. `make set-version` rewrites only
 * `secondarySources` (`src/go/cmd/anypick-release/version.go`), so a number
 * written here is invisible to it and drifts at the next bump.
 */
export function helloFrame(
  nodePlatform: string,
  appVersion: string,
): string {
  const hostHello = {
    protocolVersion: PROTOCOL_VERSION,
    appVersion,
    platform: hostPlatform(nodePlatform),
    capabilities: [
      'command-dto-v1',
      'snapshot-dto-v1',
      'cancel-v1',
      'doctor-v1',
      'explicit-refresh',
      'host-effects-v1',
      'post-mutation-events',
      'usage-events-v1',
    ],
    hostState: { launchAtLogin: false },
  };
  return `hello\t${Buffer.from(JSON.stringify(hostHello)).toString('base64')}\n`;
}

export function devServicePlugin(repoRoot: string): Plugin {
  return {
    name: 'dev-service',
    configureServer(server) {
      // Read once. A missing tauri.conf.json must not throw inside the stdout
      // handler below, where it would take down the dev server rather than
      // just logging.
      let appVersion: string;
      try {
        appVersion = readTauriVersion(repoRoot);
      } catch (err) {
        console.warn('[dev-service] Could not read the app version:', err);
        return;
      }

      const candidates = [
        resolve(repoRoot, 'src/tauri/app/binaries/anypick-core-aarch64-apple-darwin'),
        resolve(repoRoot, 'src/tauri/app/target/debug/anypick-core'),
        resolve(repoRoot, 'src/go/bin/anypick-dev'),
      ];

      const binaryPath = candidates.find((p) => existsSync(p));
      if (!binaryPath) {
        console.warn('[dev-service] No anypick core binary found. Running in mock-only mode.');
        return;
      }

      console.log(`[dev-service] Found binary: ${binaryPath}`);

      let child: ChildProcess | null = null;

      function startProcess() {
        if (child) {
          try {
            child.kill();
          } catch {}
        }

        console.log('[dev-service] Spawning desktop-service --stdio...');
        child = spawn(binaryPath!, ['desktop-service', '--stdio'], {
          stdio: ['pipe', 'pipe', 'inherit'],
          env: process.env,
        });

        let buffer = '';

        child.stdout?.on('data', (chunk: Buffer) => {
          buffer += chunk.toString();
          let idx: number;
          while ((idx = buffer.indexOf('\n')) !== -1) {
            const line = buffer.slice(0, idx).trim();
            buffer = buffer.slice(idx + 1);
            if (!line) continue;

            const tab = line.indexOf('\t');
            const kind = tab === -1 ? line : line.slice(0, tab);

            if (kind === 'hello') {
              const frame = helloFrame(process.platform, appVersion);
              child?.stdin?.write(frame);
            } else if (kind === 'ready') {
              console.log('[dev-service] Handshake ready. Connected to real AnyPick service.');
            } else {
              // The SSE bridge these lines once fed (/_api/*) is gone with the
              // browser bridge; a dev-only log keeps the protocol observable.
              console.debug(`[dev-service] ${line}`);
            }
          }
        });

        child.on('exit', (code) => {
          console.warn(`[dev-service] desktop-service exited with code ${code}`);
          child = null;
        });

        child.on('error', (err) => {
          console.error('[dev-service] Failed to start desktop-service:', err);
          child = null;
        });
      }

      startProcess();

      server.httpServer?.on('close', () => {
        if (child) {
          try {
            child.stdin?.write('quit\t\n');
            child.kill();
          } catch {}
          child = null;
        }
      });

      server.middlewares.use((req: IncomingMessage, res: ServerResponse, next: () => void) => {
        const url = req.url || '';

        next();
      });
    },
  };
}
