export interface TauriBuildOptions {
  bundles?: string[];
  noBundle: boolean;
  target?: string;
}

export interface ExecutableMetadata {
  architecture: 'arm64' | 'x64';
  format: 'ELF' | 'Mach-O' | 'PE';
}

export interface InspectedExecutable extends ExecutableMetadata {
  bytes: number;
  path: string;
}

export interface UnsignedArtifact {
  bytes: number;
  path: string;
  sha256: string;
  type: 'file' | 'symlink';
}

export interface StagedBundle {
  artifacts: UnsignedArtifact[];
  manifestPath: string;
}

export function buildTauriArgs(options: TauriBuildOptions): string[];
export function executableMetadata(buffer: Buffer, target: string): ExecutableMetadata;
export function hostTarget(rustcOutput: string): string;
export function inspectExecutable(path: string, target: string): InspectedExecutable;
export function parseBuildArgs(argv: string[]): TauriBuildOptions;
export function run(argv?: string[]): void;
export function stageBundleArtifacts(input: {
  bundleRoot: string;
  destination: string;
  target: string;
}): StagedBundle;
export function validateTarget(target: string): string;
