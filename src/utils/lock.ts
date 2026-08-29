/**
 * Scoped file locks for concurrent mutations (spec §23.3).
 *
 * Lock files are durable coordination points, not ownership records.  The
 * actual exclusion is an advisory `flock(2)` on the open file descriptor, so
 * the kernel releases it if a process dies.  Keeping the inode in place avoids
 * the ABA race inherent in `read stale pid → unlink lock → create lock`.
 */

import { mkdir, open, readFile, rename, rm, writeFile } from 'node:fs/promises';
import { dirname } from 'node:path';
import { randomUUID } from 'node:crypto';
import { flock } from 'fs-ext';
import { anypickError, ExitCode } from './errors';

export interface LockInfo {
  pid: number;
  startedAt: string;
  /** Random ownership nonce prevents PID-reuse ABA on release. */
  ownerId?: string;
}

export interface WithFileLockOptions {
  /** Max wait for another holder (ms). Default 5000. */
  timeoutMs?: number;
  /** Poll interval while waiting (ms). Default 25. */
  pollMs?: number;
  /** Human label for errors. */
  resource?: string;
}

// flock(2) excludes other processes, but some kernels treat locks acquired by
// different descriptors in the *same* process as one owner. Keep a tiny local
// queue as well so concurrent promises in a CLI/TUI process cannot bypass the
// coordinator before the advisory lock protects cross-process callers.
const localTails = new Map<string, Promise<void>>();

function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms));
}

export async function readLockInfo(lockPath: string): Promise<LockInfo | null> {
  try {
    const raw = await readFile(lockPath, 'utf8');
    const data = JSON.parse(raw) as Partial<LockInfo>;
    if (typeof data.pid === 'number' && data.pid > 0) {
      return {
        pid: data.pid,
        startedAt: typeof data.startedAt === 'string' ? data.startedAt : '',
        ownerId: typeof data.ownerId === 'string' ? data.ownerId : undefined,
      };
    }
    return null;
  } catch {
    return null;
  }
}

/**
 * Advisory locks disappear with their file descriptors, including after a
 * crash, so there is no stale-lock takeover path.  Kept for doctor callers
 * that support pre-1.0 dot-lock records; a durable lock file is harmless when
 * no descriptor holds it.
 */
export async function isLockStale(lockPath: string): Promise<boolean> {
  void lockPath;
  return false;
}

function lock(fd: number, flags: 'exnb' | 'un'): Promise<void> {
  return new Promise((resolve, reject) => {
    flock(fd, flags, (err) => {
      if (err) {
        reject(err);
      } else {
        resolve();
      }
    });
  });
}

function lockUnavailable(err: unknown): boolean {
  return (
    err instanceof Error &&
    'code' in err &&
    (err.code === 'EAGAIN' || err.code === 'EACCES' || err.code === 'EWOULDBLOCK')
  );
}

async function enterLocalQueue(
  lockPath: string,
  deadline: number,
  resource: string,
): Promise<() => void> {
  const previous = localTails.get(lockPath);
  let release!: () => void;
  const mine = new Promise<void>((resolve) => {
    release = resolve;
  });
  localTails.set(lockPath, mine);

  if (previous) {
    const remaining = deadline - Date.now();
    const acquired =
      remaining > 0
        ? await Promise.race([previous.then(() => true), sleep(remaining).then(() => false)])
        : false;
    if (!acquired) {
      // Do not unlink this queue node: later waiters may already be waiting on
      // it. Release it only after its predecessor leaves, preserving FIFO even
      // though this caller has timed out.
      void previous.finally(release);
      const info = await readLockInfo(lockPath);
      throw stateConflict(lockPath, resource, info);
    }
  }

  return () => {
    release();
    if (localTails.get(lockPath) === mine) {
      localTails.delete(lockPath);
    }
  };
}

function stateConflict(lockPath: string, resource: string, info: LockInfo | null): Error {
  return anypickError(
    `Resource locked: ${resource}` +
      (info ? ` (held by pid ${info.pid})` : '') +
      '. Retry after the other anypick process finishes.',
    'STATE_CONFLICT',
    {
      exitCode: ExitCode.CAPABILITY_CONFLICT,
      details: { lockPath, ownerPid: info?.pid },
    },
  );
}

/**
 * Acquire an exclusive advisory lock, run fn, then release it.  The lock file
 * deliberately remains on disk after release: unlinking a pathname while a
 * waiter still has its inode open can split one logical lock into two files.
 * Kernel-managed ownership makes stale PID detection and lock stealing
 * unnecessary.
 */
export async function withFileLock<T>(
  lockPath: string,
  fn: () => Promise<T>,
  opts: WithFileLockOptions = {},
): Promise<T> {
  const timeoutMs = opts.timeoutMs ?? 5000;
  const pollMs = opts.pollMs ?? 25;
  const resource = opts.resource ?? lockPath;
  const deadline = Date.now() + timeoutMs;

  await mkdir(dirname(lockPath), { recursive: true, mode: 0o700 });
  const leaveLocalQueue = await enterLocalQueue(lockPath, deadline, resource);
  const ownerId = randomUUID();
  let handle: Awaited<ReturnType<typeof open>> | undefined;
  try {
    handle = await open(lockPath, 'a+', 0o600);
    while (true) {
      try {
        await lock(handle.fd, 'exnb');
        break;
      } catch (err) {
        if (!lockUnavailable(err)) {
          throw err;
        }
        if (Date.now() >= deadline) {
          const info = await readLockInfo(lockPath);
          throw stateConflict(lockPath, resource, info);
        }
        await sleep(pollMs);
      }
    }

    const info: LockInfo = {
      pid: process.pid,
      startedAt: new Date().toISOString(),
      ownerId,
    };
    await handle.truncate(0);
    await handle.writeFile(JSON.stringify(info) + '\n', { encoding: 'utf8' });
    await handle.sync();
    return await fn();
  } finally {
    if (handle) {
      try {
        await lock(handle.fd, 'un');
      } catch {
        // Closing the descriptor still releases an advisory lock.
      }
      try {
        await handle.close();
      } catch {
        // best-effort close during shutdown
      }
    }
    leaveLocalQueue();
  }
}

/**
 * Atomically write a file under an exclusive lock sibling (`path.lock`).
 */
export async function writeFileLocked(path: string, content: string, mode = 0o600): Promise<void> {
  await withFileLock(`${path}.lock`, async () => {
    await mkdir(dirname(path), { recursive: true, mode: 0o700 });
    const tmp = `${path}.${randomUUID()}.tmp`;
    try {
      await writeFile(tmp, content, { mode, flag: 'wx' });
      await rename(tmp, path);
    } catch (err) {
      await rm(tmp, { force: true }).catch(() => {});
      throw err;
    }
  });
}
