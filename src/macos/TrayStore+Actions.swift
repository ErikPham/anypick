import AppKit
import Foundation
import SwiftUI
/// App, route, and usage queries.
@MainActor
extension TrayStore {
  func appActions(_ client: String) -> [ActionSnapshot] {
    actions.filter { $0.enabled && $0.presentation == "app-route" && $0.client == client }
  }

  func managedActions(clientId: String) -> [ActionSnapshot] {
    actions.filter { $0.enabled && $0.clientId == clientId }
  }

  func nativeActions(_ client: String) -> [ActionSnapshot] {
    nativeAccounts.filter { $0.client == client }
  }

  func nativeSourceActions(_ sourceId: String) -> [ActionSnapshot] {
    nativeAccounts.filter { $0.sourceId == sourceId }
  }

  func route(_ client: String) -> RouteSnapshot? {
    routes.first { $0.client == client }
  }

  func route(clientId: String) -> RouteSnapshot? {
    routes.first { $0.clientId == clientId }
  }

  func modelConfig(_ client: String) -> ClientModelConfigSnapshot? {
    clientModelConfigs.first { $0.client == client || $0.clientId == client }
  }

  /// Match usage cards by short client name, display name, or source id.
  func usage(for client: String) -> UsageSnapshot? {
    let needle = client.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !needle.isEmpty else { return nil }
    let display = anypickDisplayName(needle)
    return usage.first { card in
      card.client.caseInsensitiveCompare(needle) == .orderedSame
        || card.client.caseInsensitiveCompare(display) == .orderedSame
        || card.account.localizedCaseInsensitiveContains(needle)
        || needle.localizedCaseInsensitiveContains(card.client)
    }
  }
}
