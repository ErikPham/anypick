import type { Plugin } from 'vite';
import { spawn, type ChildProcess } from 'node:child_process';
import { existsSync } from 'node:fs';
import { resolve } from 'node:path';
import type { IncomingMessage, ServerResponse } from 'node:http';

export function devServicePlugin(repoRoot: string): Plugin {
  return {
    name: 'dev-service',
    configureServer(server) {
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
      let lastLine: string | null = null;
      let lastSnapshotLine: string | null = null;
      let isReady = false;
      const sseClients = new Set<ServerResponse>();

      function broadcast(line: string) {
        lastLine = line;
        if (line.startsWith('snapshot\t') || line.startsWith('snapshot-event\t')) {
          lastSnapshotLine = line;
        }
        const msg = `data: ${JSON.stringify(line)}\n\n`;
        for (const client of sseClients) {
          try {
            client.write(msg);
          } catch {
            sseClients.delete(client);
          }
        }
      }

      function startProcess() {
        if (child) {
          try {
            child.kill();
          } catch {}
        }

        isReady = false;
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
              const platform =
                process.platform === 'darwin'
                  ? 'macos'
                  : process.platform === 'win32'
                    ? 'windows'
                    : 'linux';
              const hostHello = {
                protocolVersion: 2,
                appVersion: '1.0.1',
                platform,
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
              const frame = `hello\t${Buffer.from(JSON.stringify(hostHello)).toString('base64')}\n`;
              child?.stdin?.write(frame);
            } else if (kind === 'ready') {
              isReady = true;
              console.log('[dev-service] Handshake ready. Connected to real AnyPick service.');
            } else {
              broadcast(line);
            }
          }
        });

        child.on('exit', (code) => {
          console.warn(`[dev-service] desktop-service exited with code ${code}`);
          child = null;
          isReady = false;
        });

        child.on('error', (err) => {
          console.error('[dev-service] Failed to start desktop-service:', err);
          child = null;
          isReady = false;
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

        if (url === '/_api/service-events') {
          res.writeHead(200, {
            'Content-Type': 'text/event-stream',
            'Cache-Control': 'no-cache, no-transform',
            Connection: 'keep-alive',
            'Access-Control-Allow-Origin': '*',
          });
          sseClients.add(res);

          if (lastSnapshotLine) {
            res.write(`data: ${JSON.stringify(lastSnapshotLine)}\n\n`);
          } else if (lastLine) {
            res.write(`data: ${JSON.stringify(lastLine)}\n\n`);
          }

          req.on('close', () => {
            sseClients.delete(res);
          });
          return;
        }

        if (url === '/_api/last-line') {
          res.writeHead(200, {
            'Content-Type': 'application/json',
            'Access-Control-Allow-Origin': '*',
          });
          res.end(JSON.stringify({ line: lastSnapshotLine ?? lastLine, ready: isReady }));
          return;
        }

        if (url === '/_api/command' && req.method === 'POST') {
          let body = '';
          req.on('data', (c) => (body += c));
          req.on('end', () => {
            try {
              const { command } = JSON.parse(body);
              if (command && child?.stdin?.writable) {
                child.stdin.write(`${command}\n`);
                res.writeHead(200, {
                  'Content-Type': 'application/json',
                  'Access-Control-Allow-Origin': '*',
                });
                res.end(JSON.stringify({ ok: true }));
                return;
              }
            } catch {}
            res.writeHead(400, { 'Content-Type': 'application/json' });
            res.end(JSON.stringify({ error: 'invalid command' }));
          });
          return;
        }


        next();
      });
    },
  };
}
