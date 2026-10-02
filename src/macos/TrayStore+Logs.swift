import AppKit
import Foundation
import SwiftUI
/// Log polling and sources.
@MainActor
extension TrayStore {
  func requestLogs(_ source: LogSourceSnapshot, lines: Int = 160, silent: Bool = false) {
    requestLogs(
      id: source.id,
      providerId: source.providerId,
      name: source.name,
      lines: lines,
      silent: silent
    )
  }

  func selectLogSource(_ sourceId: String) {
    guard logSources.contains(where: { $0.id == sourceId }) else { return }
    selectedLogSourceId = sourceId
  }

  func requestLogs(_ proxy: ProxySnapshot, lines: Int = 80, silent: Bool = false) {
    requestLogs(
      id: proxy.id,
      providerId: proxy.providerId,
      name: proxy.id.components(separatedBy: "/").dropFirst().joined(separator: "/"),
      lines: lines,
      silent: silent
    )
  }

  /// Poll the selected log source while the Logs panel is visible.
  func startLogPolling(for sourceId: String) {
    guard logPollSourceId != sourceId else { return }
    stopLogPolling()
    logPollSourceId = sourceId
    logPollTask = Task { @MainActor [weak self] in
      while !Task.isCancelled {
        guard let self else { return }
        guard self.logPollSourceId == sourceId else { return }
        if let source = self.logSources.first(where: { $0.id == sourceId }),
           self.loadingLogRequestId == nil {
          self.requestLogs(source, silent: true)
        }
        try? await Task.sleep(nanoseconds: 1_750_000_000)
      }
    }
  }

  func stopLogPolling() {
    logPollTask?.cancel()
    logPollTask = nil
    logPollSourceId = nil
  }

  /// Ends the in-flight log request, whichever way it ended. A log request has
  /// its own slot, so a rejected frame or an unreadable payload must release it
  /// too — otherwise log polling is blocked until the 10s timeout and the
  /// message blames the wrong thing.
  func finishLogRequest() {
    logRequestTimeoutTask?.cancel()
    logRequestTimeoutTask = nil
    loadingLogSourceId = nil
    loadingLogRequestId = nil
  }

  private func requestLogs(
    id: String,
    providerId: String,
    name: String,
    lines: Int,
    silent: Bool = false
  ) {
    // Foreground loads own loadingLogSourceId (drives the Loading UI).
    // Background polls must not touch it: flipping to .loading on every
    // 1.75s tick swaps the notice row and hides the Live badge — the jump.
    // Either kind still needs a free request slot to match the response.
    guard loadingLogRequestId == nil else { return }
    if !silent {
      guard loadingLogSourceId == nil else { return }
    }
    let requestId = UUID().uuidString
    let request = ProxyLogsRequest(
      requestId: requestId,
      providerId: providerId,
      name: name,
      lines: lines
    )
    guard let data = encodeCommand(request) else { return }
    logRequestTimeoutTask?.cancel()
    if !silent {
      loadingLogSourceId = id
    }
    loadingLogRequestId = requestId
    emit("logs\t\(data.base64EncodedString())")
    logRequestTimeoutTask = Task { @MainActor [weak self] in
      try? await Task.sleep(nanoseconds: 10_000_000_000)
      guard !Task.isCancelled,
            let self,
            self.loadingLogRequestId == requestId else { return }
      // Background polls fail quietly; keep the last good tail on screen.
      if !silent {
        // The engine answers a slow log read itself (its own budget is shorter
        // than this one), so reaching here means the answer was lost. Say that
        // instead of blaming the request.
        self.logStatesBySource[id] = "error"
        self.showMessage("The log request got no answer. Refresh to try again.", isError: true)
      }
      self.loadingLogSourceId = nil
      self.loadingLogRequestId = nil
    }
  }
}
