import Foundation
import OSLog
import ServiceManagement

enum GoServiceProcessError: LocalizedError {
  case helperMissing(URL)
  case helperNotExecutable(URL)
  case pipeUnavailable

  var errorDescription: String? {
    switch self {
    case .helperMissing:
      "The bundled AnyPick service is missing. Reinstall AnyPick."
    case .helperNotExecutable:
      "The bundled AnyPick service cannot be launched. Reinstall AnyPick."
    case .pipeUnavailable:
      "AnyPick could not create a private connection to its service."
    }
  }
}

/// Owns the bundled Go child, its dedicated pipes, bounded restart policy and
/// graceful shutdown. Production resolution is bundle-relative only.
actor GoServiceProcess {
  typealias FrameHandler = @MainActor @Sendable (Data) -> Void
  typealias StateHandler = @MainActor @Sendable (AppServiceConnectionState) -> Void

  private static let log = Logger(subsystem: "app.anypick.desktop", category: "go-service")
  private static let maxRestartCount = 4
  private static let restartDelays: [UInt64] = [250, 500, 1_000, 2_000]

  private let client: AppServiceClient
  private let onFrame: FrameHandler
  private let onState: StateHandler
  private var process: Process?
  private var input: Pipe?
  private var output: Pipe?
  private var errors: Pipe?
  private var generation = UUID()
  private var desiredRunning = false
  private var terminalDegraded = false
  private var restartCount = 0
  private var pendingCommands: [String] = []
  private var restartTask: Task<Void, Never>?
  private var stabilityTask: Task<Void, Never>?

  init(
    appVersion: String,
    launchAtLogin: Bool,
    onFrame: @escaping FrameHandler,
    onState: @escaping StateHandler
  ) {
    client = AppServiceClient(appVersion: appVersion, launchAtLogin: launchAtLogin)
    self.onFrame = onFrame
    self.onState = onState
  }

  func start() async {
    guard !desiredRunning else { return }
    desiredRunning = true
    terminalDegraded = false
    restartCount = 0
    await launch()
  }

  func send(_ command: String) async {
    guard desiredRunning else { return }
    guard command.lengthOfBytes(using: .utf8) <= 16 * 1024 else {
      await publish(.degraded("A command exceeded the safe service frame limit."))
      return
    }
    guard await client.isReady(), process?.isRunning == true else {
      if pendingCommands.count < 64 { pendingCommands.append(command) }
      return
    }
    write(command)
  }

  func stop() async {
    desiredRunning = false
    terminalDegraded = false
    restartTask?.cancel()
    stabilityTask?.cancel()
    pendingCommands.removeAll()
    guard let process else {
      await publish(.stopped)
      return
    }
    if process.isRunning {
      write("quit")
      for _ in 0..<20 where process.isRunning {
        try? await Task.sleep(nanoseconds: 50_000_000)
      }
      if process.isRunning { process.terminate() }
    }
    clearPipeHandlers()
    self.process = nil
    await publish(.stopped)
  }

  private func launch() async {
    await publish(.starting)
    await client.reset()
    do {
      let helper = try Self.helperURL()
      let process = Process()
      let input = Pipe()
      let output = Pipe()
      let errors = Pipe()
      let launchGeneration = UUID()
      generation = launchGeneration

      process.executableURL = helper
      process.arguments = ["desktop-service", "--stdio"]
      process.standardInput = input
      process.standardOutput = output
      process.standardError = errors
      output.fileHandleForReading.readabilityHandler = { [weak self] handle in
        let data = handle.availableData
        guard !data.isEmpty else { return }
        Task { await self?.receive(data, generation: launchGeneration) }
      }
      errors.fileHandleForReading.readabilityHandler = { handle in
        let data = handle.availableData
        guard !data.isEmpty, let message = String(data: data, encoding: .utf8) else { return }
        Self.log.error("\(message.prefix(2_000), privacy: .private)")
      }
      process.terminationHandler = { [weak self] terminated in
        let status = terminated.terminationStatus
        Task { await self?.terminated(status: status, generation: launchGeneration) }
      }

      self.process = process
      self.input = input
      self.output = output
      self.errors = errors
      try process.run()
    } catch {
      clearPipeHandlers()
      process = nil
      await scheduleRestart(after: error.localizedDescription)
    }
  }

  private func receive(_ data: Data, generation: UUID) async {
    guard generation == self.generation, desiredRunning else { return }
    do {
      let actions = try await client.consume(data)
      for action in actions {
        switch action {
        case .send(let line):
          write(line)
        case .deliver(let frame):
          await onFrame(frame)
        case .state(let state):
          await publish(state)
          if state == .ready {
            flushPendingCommands()
            beginStabilityWindow(generation: generation)
          }
        case .hostEffect(let requestId, let effect):
          let result = await MacOSHostEffects.perform(effect)
          let response = try await client.hostEffectResultLine(
            requestId: requestId,
            status: result.success ? "success" : "error",
            message: result.message,
            launchAtLogin: result.launchAtLogin
          )
          write(response)
        }
      }
    } catch {
      terminalDegraded = true
      desiredRunning = false
      pendingCommands.removeAll()
      await publish(.degraded(error.localizedDescription))
      process?.terminate()
    }
  }

  private func terminated(status: Int32, generation: UUID) async {
    guard generation == self.generation else { return }
    clearPipeHandlers()
    process = nil
    input = nil
    output = nil
    errors = nil
    stabilityTask?.cancel()
    if !desiredRunning {
      if !terminalDegraded { await publish(.stopped) }
      return
    }
    await scheduleRestart(after: "The AnyPick service exited with status \(status).")
  }

  private func scheduleRestart(after message: String) async {
    guard desiredRunning else { return }
    guard restartCount < Self.maxRestartCount else {
      terminalDegraded = true
      desiredRunning = false
      await publish(.degraded("\(message) Restart limit reached."))
      return
    }
    let delay = Self.restartDelays[restartCount]
    restartCount += 1
    await publish(.degraded("\(message) Restarting the bundled service…"))
    restartTask?.cancel()
    restartTask = Task { [weak self] in
      try? await Task.sleep(nanoseconds: delay * 1_000_000)
      guard !Task.isCancelled else { return }
      await self?.launchIfWanted()
    }
  }

  private func launchIfWanted() async {
    guard desiredRunning, process == nil else { return }
    await launch()
  }

  private func beginStabilityWindow(generation: UUID) {
    stabilityTask?.cancel()
    stabilityTask = Task { [weak self] in
      try? await Task.sleep(nanoseconds: 30_000_000_000)
      guard !Task.isCancelled else { return }
      await self?.markStable(generation: generation)
    }
  }

  private func markStable(generation: UUID) {
    guard generation == self.generation, process?.isRunning == true else { return }
    restartCount = 0
  }

  private func flushPendingCommands() {
    let commands = pendingCommands
    pendingCommands.removeAll()
    for command in commands { write(command) }
  }

  private func write(_ line: String) {
    guard let handle = input?.fileHandleForWriting else { return }
    do {
      try handle.write(contentsOf: Data("\(line)\n".utf8))
    } catch {
      Self.log.error("service pipe write failed: \(error.localizedDescription, privacy: .public)")
    }
  }

  private func clearPipeHandlers() {
    output?.fileHandleForReading.readabilityHandler = nil
    errors?.fileHandleForReading.readabilityHandler = nil
  }

  private func publish(_ state: AppServiceConnectionState) async {
    await onState(state)
  }

  nonisolated private static func helperURL() throws -> URL {
#if DEBUG
    if let override = ProcessInfo.processInfo.environment["ANYPICK_CORE_HELPER"],
       !override.isEmpty {
      let url = URL(fileURLWithPath: override)
      guard FileManager.default.fileExists(atPath: url.path) else {
        throw GoServiceProcessError.helperMissing(url)
      }
      guard FileManager.default.isExecutableFile(atPath: url.path) else {
        throw GoServiceProcessError.helperNotExecutable(url)
      }
      return url
    }
#endif
    let url = Bundle.main.bundleURL
      .appendingPathComponent("Contents", isDirectory: true)
      .appendingPathComponent("Helpers", isDirectory: true)
      .appendingPathComponent("anypick-core", isDirectory: false)
    guard FileManager.default.fileExists(atPath: url.path) else {
      throw GoServiceProcessError.helperMissing(url)
    }
    guard FileManager.default.isExecutableFile(atPath: url.path) else {
      throw GoServiceProcessError.helperNotExecutable(url)
    }
    return url
  }
}

private struct MacOSHostEffectExecution: Sendable {
  let success: Bool
  let message: String?
  let launchAtLogin: Bool
}

@MainActor
enum MacOSHostEffects {
  static var launchAtLoginEnabled: Bool {
    SMAppService.mainApp.status == .enabled
  }

  fileprivate static func perform(_ effect: AppHostEffect) -> MacOSHostEffectExecution {
    guard effect.kind == "launch-at-login" else {
      return MacOSHostEffectExecution(
        success: false,
        message: "AnyPick does not support that system operation.",
        launchAtLogin: launchAtLoginEnabled
      )
    }
    let service = SMAppService.mainApp
    do {
      if effect.enabled {
        if service.status != .enabled { try service.register() }
      } else if service.status != .notRegistered {
        try service.unregister()
      }
      let enabled = service.status == .enabled
      if effect.enabled, service.status == .requiresApproval {
        return MacOSHostEffectExecution(
          success: false,
          message: "Allow AnyPick in System Settings › General › Login Items, then try again.",
          launchAtLogin: false
        )
      }
      guard enabled == effect.enabled else {
        return MacOSHostEffectExecution(
          success: false,
          message: "macOS did not apply the Login Items change.",
          launchAtLogin: enabled
        )
      }
      return MacOSHostEffectExecution(
        success: true,
        message: nil,
        launchAtLogin: enabled
      )
    } catch {
      return MacOSHostEffectExecution(
        success: false,
        message: String(error.localizedDescription.prefix(300)),
        launchAtLogin: service.status == .enabled
      )
    }
  }
}
