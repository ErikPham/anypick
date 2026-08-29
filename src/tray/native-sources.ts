import { readFile, readdir } from 'node:fs/promises';
import { join } from 'node:path';

/**
 * Ordered Swift sources for the macOS app (UI + supervisor store).
 * Prefer sources.txt so compile order stays stable across machines.
 */
export async function macosSwiftFiles(macosDir: string): Promise<string[]> {
  return swiftFilesFromManifest(macosDir);
}

/** @deprecated Use macosSwiftFiles. Kept for tests that have not switched. */
export async function nativeTraySwiftFiles(macosDir: string): Promise<string[]> {
  return macosSwiftFiles(macosDir);
}

async function swiftFilesFromManifest(directory: string): Promise<string[]> {
  const manifest = join(directory, 'sources.txt');
  try {
    const text = await readFile(manifest, 'utf8');
    const names = text
      .split(/\r?\n/)
      .map((line) => line.trim())
      .filter((line) => line && !line.startsWith('#') && line.endsWith('.swift'));
    if (names.length > 0) {
      return names.map((name) => join(directory, name));
    }
  } catch {
    // fall through
  }
  const entries = await readdir(directory);
  return entries
    .filter((name) => name.endsWith('.swift'))
    .sort()
    .map((name) => join(directory, name));
}

export async function readMacosSwiftBundle(macosDir: string): Promise<string> {
  const files = await macosSwiftFiles(macosDir);
  const chunks = await Promise.all(files.map((file) => readFile(file, 'utf8')));
  return chunks.join('\n\n');
}

/** @deprecated Use readMacosSwiftBundle. */
export async function readNativeTraySwiftBundle(macosDir: string): Promise<string> {
  return readMacosSwiftBundle(macosDir);
}
