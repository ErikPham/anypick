import { EventEmitter } from 'node:events';
import { mkdtempSync, writeFileSync, mkdirSync, readFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it, vi } from 'vitest';

import { devServicePlugin, helloFrame } from '../scripts/dev-service-plugin';
import { appVersionDefine, readTauriVersion } from '../scripts/app-version';

const here = dirname(fileURLToPath(import.meta.url));

function repoWith(version: string): string {
  const root = mkdtempSync(join(tmpdir(), 'anypick-version-'));
  mkdirSync(join(root, 'src/tauri/app'), { recursive: true });
  writeFileSync(
    join(root, 'src/tauri/app/tauri.conf.json'),
    JSON.stringify({ productName: 'AnyPick', version }),
  );
  return root;
}

function decodeHello(frame: string): Record<string, unknown> {
  const [kind, payload] = frame.split('\t');
  expect(kind).toBe('hello');
  return JSON.parse(Buffer.from(payload!.trim(), 'base64').toString('utf8')) as Record<
    string,
    unknown
  >;
}

describe('dev-service hello frame', () => {
  it('reports the version tauri.conf.json declares, not a literal', () => {
    // 9.9.9 is not any released version, so a surviving literal cannot match.
    expect(decodeHello(helloFrame('darwin', readTauriVersion(repoWith('9.9.9')))).appVersion).toBe(
      '9.9.9',
    );
  });

  it('tracks a bump instead of reporting the build it was written against', () => {
    const root = repoWith('1.0.1');
    const before = decodeHello(helloFrame('darwin', readTauriVersion(root))).appVersion;
    writeFileSync(
      join(root, 'src/tauri/app/tauri.conf.json'),
      JSON.stringify({ productName: 'AnyPick', version: '1.1.0' }),
    );
    const after = decodeHello(helloFrame('darwin', readTauriVersion(root))).appVersion;

    expect(before).toBe('1.0.1');
    expect(after).toBe('1.1.0');
  });

  it('keeps the handshake fields the engine validates', () => {
    const hello = decodeHello(helloFrame('darwin', '1.1.0'));

    expect(hello.platform).toBe('macos');
    expect(hello.capabilities).toEqual([
      'command-dto-v1',
      'snapshot-dto-v1',
      'cancel-v1',
      'doctor-v1',
      'explicit-refresh',
      'host-effects-v1',
      'post-mutation-events',
      'usage-events-v1',
    ]);
    expect(hello.hostState).toEqual({ launchAtLogin: false });
  });

  it('refuses to start rather than throwing inside the stdout handler', () => {
    // A missing tauri.conf.json used to be impossible here, because the frame
    // carried a literal. Now that it is read, the plugin has to fail closed at
    // setup, or the read throws inside a 'data' event and takes the dev server
    // down instead of logging.
    const root = repoWith('1.0.0');
    writeFileSync(join(root, 'src/tauri/app/tauri.conf.json'), 'not json');

    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    const plugin = devServicePlugin(root);
    const configure = plugin.configureServer as (server: unknown) => void;

    expect(() =>
      configure({ httpServer: new EventEmitter(), middlewares: { use: vi.fn() } }),
    ).not.toThrow();
    expect(warn).toHaveBeenCalled();
  });

  it('sends the protocol version by reference, never as a bare number', () => {
    // `make check-protocol` reads the Go authority, Rust, Swift and wire.ts.
    // It cannot see this frame, so a literal here would drift uncaught, and
    // comparing the sent value against PROTOCOL_VERSION cannot catch it either
    // while the two agree. Syntactic, so treat it as a tripwire.
    const source = readFileSync(join(here, '..', 'scripts', 'dev-service-plugin.ts'), 'utf8');

    expect(source).toMatch(/protocolVersion:\s*PROTOCOL_VERSION/);
    expect(source).not.toMatch(/protocolVersion:\s*[0-9]/);
  });

  it('is the frame the plugin actually writes to the engine', () => {
    // The cases above pin helloFrame. This one pins the wiring, so a change
    // that re-inlines a literal payload inside configureServer, or stops using
    // helloFrame, cannot leave them green.
    const source = readFileSync(join(here, '..', 'scripts', 'dev-service-plugin.ts'), 'utf8');

    expect(source).toMatch(/helloFrame\(process\.platform, appVersion\)/);
    // One payload key, and it is the bare identifier rather than a literal.
    expect(source.match(/^\s+appVersion,/gm)).toHaveLength(1);
  });

  it('names the platform the engine expects for each host OS', () => {
    expect(decodeHello(helloFrame('darwin', '1.1.0')).platform).toBe('macos');
    expect(decodeHello(helloFrame('win32', '1.1.0')).platform).toBe('windows');
    expect(decodeHello(helloFrame('linux', '1.1.0')).platform).toBe('linux');
  });

  it('terminates the frame with a newline, since the engine reads line-delimited', () => {
    expect(helloFrame('darwin', '1.1.0')).toMatch(/\n$/);
  });
});

describe('readTauriVersion', () => {
  it('throws rather than reporting a blank version to the service', () => {
    // A silent empty string is a handshake claiming a version the app lacks.
    const root = repoWith('1.0.0');
    writeFileSync(
      join(root, 'src/tauri/app/tauri.conf.json'),
      JSON.stringify({ productName: 'AnyPick' }),
    );

    expect(() => readTauriVersion(root)).toThrow(/no version/);
  });

  it('feeds both the browser define and the dev handshake from one read', () => {
    // The About footer and the hello frame must never name different builds.
    const root = repoWith('2.3.4');

    expect(JSON.parse(appVersionDefine(root).__ANYPICK_VERSION__)).toBe(readTauriVersion(root));
    expect(decodeHello(helloFrame('darwin', readTauriVersion(root))).appVersion).toBe(
      readTauriVersion(root),
    );
  });
});
