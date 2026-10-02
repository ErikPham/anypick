import Foundation

/// Ordered hand-off for bytes read from the engine's stdout pipe.
///
/// `FileHandle.readabilityHandler` delivers whatever the pipe has, and a single
/// frame is routinely larger than one read (a snapshot is tens of kilobytes).
/// Handing each chunk to its own `Task` loses the guarantee that they reach the
/// protocol client in arrival order, and out-of-order chunks desynchronize the
/// line framing: the client then sees a payload fragment where it expects a
/// frame name and reports a protocol failure. Buffering the chunks under one
/// lock and draining them from a single task keeps the byte order the engine
/// wrote.
final class FrameChunkBuffer {
  private let lock = NSLock()
  private var chunks: [Data] = []
  private var draining = false

  /// Appends one read chunk. Returns true when the caller now owns the drain:
  /// exactly one drain runs at a time, and it keeps going until the buffer is
  /// empty.
  func enqueue(_ chunk: Data) -> Bool {
    lock.lock()
    defer { lock.unlock() }
    chunks.append(chunk)
    guard !draining else { return false }
    draining = true
    return true
  }

  /// Next chunk in arrival order, or nil when the buffer is drained. Clearing
  /// `draining` happens under the same lock as the emptiness check, so a chunk
  /// enqueued during the last drain is picked up rather than stranded.
  func next() -> Data? {
    lock.lock()
    defer { lock.unlock() }
    guard !chunks.isEmpty else {
      draining = false
      return nil
    }
    return chunks.removeFirst()
  }
}
