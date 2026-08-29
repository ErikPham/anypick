import { copyFile, mkdir, readFile, writeFile } from 'node:fs/promises';
import { join, resolve } from 'node:path';

const macosSourceDir = resolve('src/macos');
const macosDestDir = resolve('dist/macos');
await mkdir(macosDestDir, { recursive: true });

const macosManifest = await readFile(join(macosSourceDir, 'sources.txt'), 'utf8');
const macosFiles = macosManifest
  .split(/\r?\n/)
  .map((line) => line.trim())
  .filter((line) => line && !line.startsWith('#') && line.endsWith('.swift'));
if (macosFiles.length === 0) {
  throw new Error('No Swift sources listed in src/macos/sources.txt.');
}
for (const name of macosFiles) {
  await copyFile(join(macosSourceDir, name), join(macosDestDir, name));
}
await writeFile(join(macosDestDir, 'sources.txt'), `${macosFiles.join('\n')}\n`);

await mkdir(resolve('dist/tray'), { recursive: true });
await writeFile(
  resolve('dist/tray/AnyPickTray.swift'),
  [
    '// The macOS app lives in src/macos/. The build copies that tree to dist/macos/.',
    '// Do not add code here.',
    '',
  ].join('\n'),
);

const trayIconDir = resolve('dist/tray/icons');
await mkdir(trayIconDir, { recursive: true });

for (const icon of [
  'claude.svg',
  'openai.svg',
  'googlegemini.svg',
  'opencode.svg',
  'openrouter.svg',
  'kiro.svg',
  'grok.svg',
]) {
  await copyFile(resolve('src/tray/icons', icon), join(trayIconDir, icon));
}

const brandMarks = [
  'claude-mark.png',
  'codex-app-dark.png',
  'codex-app-light.png',
  'openai-mark-dark.png',
  'openai-mark-light.png',
  'gemini-mark-v2.png',
  'gemini-mark.png',
  'kiro-app.png',
  'openrouter-glyph-dark.png',
  'openrouter-glyph-light.png',
  'grok-mark-dark.png',
  'grok-mark-light.png',
  'opencode-mark-dark.png',
  'opencode-mark-light.png',
];
for (const file of brandMarks) {
  await copyFile(resolve('design/assets/brands', file), join(trayIconDir, file)).catch((error) => {
    if (error && error.code === 'ENOENT') return;
    throw error;
  });
}

for (const file of ['app-icon-32.png', 'app-icon-64.png', 'app-icon-128.png']) {
  await copyFile(resolve('design/assets/anypick', file), join(trayIconDir, file)).catch((error) => {
    if (error && error.code === 'ENOENT') return;
    throw error;
  });
}

// Dock / Cmd-Tab brand mark for the minimal AnyPick.app bundle.
const appIconDir = resolve('dist/tray/app-icons');
await mkdir(appIconDir, { recursive: true });
for (const icon of [
  'icon-16.png',
  'icon-32.png',
  'icon-64.png',
  'icon-128.png',
  'icon-256.png',
  'icon-512.png',
  'icon-1024.png',
]) {
  await copyFile(resolve('assets', icon), join(appIconDir, icon));
}
