/**
 * Compiles and launches the isolated SwiftUI macOS design prototype.
 *
 * The prototype only contains in-memory fixtures. It never starts AnyPick's
 * supervisor and never reads or writes ~/.anypick.
 */
import { spawn, spawnSync } from 'node:child_process';
import {
  cpSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';

if (process.platform !== 'darwin') {
  throw new Error('The macOS design prototype only runs on macOS.');
}

const root = resolve(import.meta.dirname, '..');
const prototypeDir = resolve(root, 'src/macos');
const compileOnly = process.argv.includes('--check');

const valueAfter = (flag) => {
  const index = process.argv.indexOf(flag);
  return index >= 0 ? process.argv[index + 1] : undefined;
};

const fixture =
  valueAfter('--fixture') || (process.argv.includes('--empty') ? 'empty' : 'populated');
if (!['populated', 'empty', 'loading', 'error'].includes(fixture)) {
  throw new Error('--fixture must be populated, empty, loading, or error.');
}

const flow = valueAfter('--flow');
const flowDestination = {
  'add-account': 'accounts',
  'add-gateway': 'accounts',
  'remove-account': 'accounts',
  'published-models': 'proxy-hub',
  'create-backup': 'backups',
  'restore-backup': 'backups',
  'import-backup': 'backups',
  'getting-started': 'overview',
  'hub-overlap': 'proxy-hub',
}[flow];
const destination = valueAfter('--destination') || flowDestination || 'overview';
const appearance = valueAfter('--appearance') || 'system';
const activityMode = valueAfter('--activity-mode') || 'events';
const renderPath = valueAfter('--render');
if (!['system', 'light', 'dark'].includes(appearance)) {
  throw new Error('--appearance must be system, light, or dark.');
}
if (!['events', 'logs'].includes(activityMode)) {
  throw new Error('--activity-mode must be events or logs.');
}
if (
  flow &&
  ![
    'add-account',
    'add-gateway',
    'remove-account',
    'published-models',
    'create-backup',
    'restore-backup',
    'import-backup',
    'getting-started',
    'hub-overlap',
    'tray',
    'tray-attention',
    'tray-use-with',
    'tray-switching',
    'tray-recovery',
    'tray-first-run',
  ].includes(flow)
) {
  throw new Error(
    '--flow must be add-account, add-gateway, remove-account, published-models, create-backup, restore-backup, import-backup, getting-started, hub-overlap, tray, tray-attention, tray-use-with, tray-switching, tray-recovery, or tray-first-run.',
  );
}

const names = readFileSync(join(prototypeDir, 'sources.txt'), 'utf8')
  .split(/\r?\n/)
  .map((line) => line.trim())
  .filter((line) => line.endsWith('.swift'));
const sources = names.map((name) => join(prototypeDir, name));

const stage = mkdtempSync(join(tmpdir(), 'anypick-macos-design-'));
const app = join(stage, 'AnyPick Design.app');
const contents = join(app, 'Contents');
const executableDir = join(contents, 'MacOS');
const binary = join(executableDir, 'AnyPick Design');
mkdirSync(executableDir, { recursive: true });

// No CFBundleIdentifier: this throwaway fixture is rebuilt at a new path on
// every launch, and a reused identifier makes Launch Services resolve it to
// stale entries from deleted stage directories, which intermittently
// suppresses SwiftUI window creation entirely.
const plist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleDevelopmentRegion</key><string>en</string>
  <key>CFBundleExecutable</key><string>AnyPick Design</string>
  <key>CFBundleInfoDictionaryVersion</key><string>6.0</string>
  <key>CFBundleName</key><string>AnyPick Design</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>LSMinimumSystemVersion</key><string>14.0</string>
  <key>NSHighResolutionCapable</key><true/>
  <key>NSHumanReadableCopyright</key><string>UI-only AnyPick prototype</string>
</dict>
</plist>
`;
writeFileSync(join(contents, 'Info.plist'), plist);

// Bundle approved brand marks + tray glyphs so DesignProviderIcon can load them.
const resourcesDir = join(contents, 'Resources');
const iconsDir = join(resourcesDir, 'icons');
const brandsDir = join(resourcesDir, 'brands');
mkdirSync(iconsDir, { recursive: true });
mkdirSync(brandsDir, { recursive: true });
const trayIcons = resolve(root, 'src/tray/icons');
const designBrands = resolve(root, 'design/assets/brands');
if (existsSync(trayIcons)) {
  for (const name of readdirSync(trayIcons)) {
    if (name.endsWith('.svg') || name.endsWith('.png')) {
      cpSync(join(trayIcons, name), join(iconsDir, name));
    }
  }
}
if (existsSync(designBrands)) {
  for (const name of readdirSync(designBrands)) {
    if (name.endsWith('.png') || name.endsWith('.svg')) {
      cpSync(join(designBrands, name), join(brandsDir, name));
      cpSync(join(designBrands, name), join(iconsDir, name));
    }
  }
}
const anypickDir = join(resourcesDir, 'anypick');
mkdirSync(anypickDir, { recursive: true });
const designAnyPick = resolve(root, 'design/assets/anypick');
if (existsSync(designAnyPick)) {
  for (const name of readdirSync(designAnyPick)) {
    if (name.startsWith('app-icon-') && name.endsWith('.png')) {
      cpSync(join(designAnyPick, name), join(anypickDir, name));
      cpSync(join(designAnyPick, name), join(iconsDir, name));
    }
  }
}

const architecture = process.arch === 'x64' ? 'x86_64' : 'arm64';
const compile = spawnSync(
  '/usr/bin/xcrun',
  [
    'swiftc',
    '-parse-as-library',
    '-D',
    'DEBUG',
    '-target',
    `${architecture}-apple-macosx14.0`,
    ...sources,
    '-o',
    binary,
    '-framework',
    'AppKit',
    '-framework',
    'SwiftUI',
  ],
  { cwd: root, stdio: 'inherit' },
);

if (compile.status !== 0) {
  rmSync(stage, { recursive: true, force: true });
  throw new Error('Could not compile the macOS design prototype.');
}

if (compileOnly) {
  process.stderr.write('[macOS design] SwiftUI prototype compiled successfully.\n');
  rmSync(stage, { recursive: true, force: true });
} else {
  process.stderr.write(
    [
      '',
      '[macOS design] UI-ONLY fixture — no AnyPick data or services are used.',
      `  Fixture: ${fixture}`,
      `  Destination: ${destination}`,
      `  Appearance: ${appearance}`,
      ...(destination === 'activity' ? [`  Activity: ${activityMode}`] : []),
      ...(flow ? [`  Flow: ${flow}`] : []),
      ...(renderPath ? [`  Render: ${resolve(renderPath)}`] : []),
      '  Tray: Menu Bar window is board 29 (Ready / Attention / Use With / First Run).',
      '  Stop with Control-C or by quitting AnyPick Design.',
      '',
    ].join('\n'),
  );

  const child = spawn(binary, [], {
    env: {
      ...process.env,
      ANYPICK_MACOS_DESIGN: '1',
      ANYPICK_MACOS_FIXTURE: fixture,
      ANYPICK_MACOS_DESTINATION: destination,
      ANYPICK_MACOS_APPEARANCE: appearance,
      ANYPICK_MACOS_ACTIVITY_MODE: activityMode,
      ANYPICK_TRAY_ICON_DIR: iconsDir,
      ...(flow ? { ANYPICK_MACOS_FLOW: flow } : {}),
      ...(renderPath ? { ANYPICK_MACOS_RENDER_PATH: resolve(renderPath) } : {}),
    },
    stdio: 'inherit',
  });

  const stop = () => child.kill('SIGTERM');
  process.once('SIGINT', stop);
  process.once('SIGTERM', stop);
  child.once('exit', (code, signal) => {
    rmSync(stage, { recursive: true, force: true });
    process.exitCode = code ?? (signal ? 1 : 0);
  });
}
