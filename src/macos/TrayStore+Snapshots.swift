import AppKit
import Foundation
import SwiftUI
/// Derived snapshot views.
@MainActor
extension TrayStore {
  var appRouteClients: [String] {
    var seen = Set<String>()
    return actions
      .filter { $0.enabled && $0.presentation == "app-route" }
      .compactMap { action in seen.insert(action.client).inserted ? action.client : nil }
  }

  var nativeAccounts: [ActionSnapshot] {
    actions.filter { $0.enabled && $0.presentation == "native-account" }
  }

  var nativeAccountClients: [String] {
    var seen = Set<String>()
    return nativeAccounts.compactMap { action in
      seen.insert(action.client).inserted ? action.client : nil
    }
  }

  var managedAppClientIDs: [String] {
    var seen = Set<String>()
    let actionable: [String] = actions.compactMap { action -> String? in
      seen.insert(action.clientId).inserted ? action.clientId : nil
    }
    let installed: [String] = accountProviders.compactMap { provider -> String? in
      guard provider.installed, let clientId = provider.clientId else { return nil }
      return seen.insert(clientId).inserted ? clientId : nil
    }
    return actionable + installed
  }

  var nativeSourceIds: [String] {
    var seen = Set<String>()
    return nativeAccounts.compactMap { action in
      seen.insert(action.sourceId).inserted ? action.sourceId : nil
    }
  }

  var primaryUsageText: String? {
    guard let window = usage.first?.windows.first else { return nil }
    return "\(window.remainingPercent)%"
  }

  var proxyHub: ProxySnapshot? {
    proxies.first { $0.providerId == "proxy-hub" }
  }

  var unavailableHubSources: [HubSourceSnapshot] {
    hubSources.filter { source in
      guard source.enabled else { return false }
      if let warning = source.warning?.trimmingCharacters(in: .whitespacesAndNewlines),
         !warning.isEmpty {
        return true
      }
      let status = source.status?.lowercased() ?? ""
      return ["attention", "error", "unavailable"].contains(status)
    }
  }

  var routingChoiceCount: Int {
    hubConflicts.filter { $0.kind == "source-choice" }.count
  }

  var routingIssueCount: Int {
    hubConflicts.filter { $0.kind != "source-choice" }.count + unavailableHubSources.count
  }

  /// Badge / menu-bar attention: routing issues + unresolved source choices.
  var attentionCount: Int {
    routingIssueCount + routingChoiceCount
  }

  /// True until AnyPick has persisted a source or route. Installed CLIs are
  /// device capabilities, not evidence that setup has happened.
  var isFirstRun: Bool {
    snapshotReady
      && accounts.isEmpty
      && gateways.isEmpty
      && !routes.contains(where: { $0.source?.isEmpty == false })
  }

  /// Installed clients come from native installation probes, not from route
  /// actions. A fresh AnyPick install has no route actions to display yet.
  var hasInstalledClient: Bool {
    accountProviders.contains { $0.clientId != nil && $0.installed }
  }

  /// Accounts exist but no installable app-route client is visible yet.
  var needsInstalledApps: Bool {
    snapshotReady
      && !accounts.isEmpty
      && !hasInstalledClient
  }

  /// Short relative freshness for chrome (“Updated just now”).
  func snapshotFreshnessText(now: Date = Date()) -> String? {
    guard let lastSnapshotAt else { return nil }
    let seconds = now.timeIntervalSince(lastSnapshotAt)
    if seconds < 8 { return "Updated just now" }
    if seconds < 60 { return "Updated \(max(1, Int(seconds)))s ago" }
    if seconds < 3600 { return "Updated \(max(1, Int(seconds / 60)))m ago" }
    return "Updated \(max(1, Int(seconds / 3600)))h ago"
  }
}
