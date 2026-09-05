import SwiftUI

/// Quick Panel system styling. Production chrome uses platform controls,
/// fonts, and colors only — no custom cards, chips, palettes, or fills.
enum TrayPaint {
  static let accent = Color.accentColor
  static let ready = Color.green
  static let attention = Color.orange
}

func traySectionTitle(_ title: String) -> some View {
  Text(title)
    .font(.caption)
    .fontWeight(.semibold)
    .foregroundStyle(.secondary)
    .textCase(.uppercase)
    .frame(maxWidth: .infinity, alignment: .leading)
}

struct TrayFillButton: View {
  let title: String
  var prominent = true
  var action: () -> Void

  var body: some View {
    if prominent {
      Button(title, action: action)
        .buttonStyle(.borderedProminent)
        .controlSize(.small)
    } else {
      Button(title, action: action)
        .buttonStyle(.bordered)
        .controlSize(.small)
    }
  }
}

struct TrayTextButton: View {
  let title: String
  var action: () -> Void

  var body: some View {
    Button(title, action: action)
      .buttonStyle(.borderless)
      .font(.caption)
      .fontWeight(.medium)
  }
}

struct TrayRoutingHeader: View {
  var showDetails = true
  var openDetails: () -> Void

  var body: some View {
    HStack(spacing: 8) {
      traySectionTitle("Proxies")
      Spacer(minLength: 4)
      if showDetails {
        Button("Details", action: openDetails)
          .buttonStyle(.borderless)
          .font(.caption)
          .fontWeight(.medium)
          .foregroundStyle(Color.accentColor)
          .accessibilityIdentifier("tray.routing.details")
      }
    }
    .frame(height: 16)
  }
}

struct TrayAttentionItem: Identifiable {
  let id: String
  let title: String
  let actionTitle: String
  let destination: DesignDestination
}

let trayPreferredPrimaryAppIDs = ["claude", "codex"]
let trayPrimaryAppLimit = 2

func trayPrimaryApps(from apps: [DesignApp]) -> [DesignApp] {
  let preferred = trayPreferredPrimaryAppIDs.compactMap { id in apps.first { $0.id == id } }
  var result = Array(preferred.prefix(trayPrimaryAppLimit))
  if result.count == trayPrimaryAppLimit { return result }
  var seen = Set(result.map(\.id))
  for app in apps where !seen.contains(app.id) {
    result.append(app)
    seen.insert(app.id)
    if result.count == trayPrimaryAppLimit { break }
  }
  return result
}

func trayOtherApps(from apps: [DesignApp]) -> [DesignApp] {
  guard apps.count > trayPrimaryAppLimit else { return [] }
  let primaryIDs = Set(trayPrimaryApps(from: apps).map(\.id))
  return apps.filter { !primaryIDs.contains($0.id) }
}

func trayAccountRoutes(in app: DesignApp) -> [DesignRoute] {
  compatibleAccountRoutes(in: app)
}

func trayGatewayRoutes(in app: DesignApp) -> [DesignRoute] {
  app.routeOptions.filter { $0.kind == .gateway }
}

@MainActor
func trayAttentionItems(from model: MacOSDesignModel) -> [TrayAttentionItem] {
  var items: [TrayAttentionItem] = []
  for app in model.apps where app.status == .attention || app.status == .unavailable {
    let shortName = app.name
      .replacingOccurrences(of: " CLI", with: "")
      .replacingOccurrences(of: " Code", with: "")
    items.append(
      TrayAttentionItem(
        id: "app.\(app.id)",
        title: app.attentionMessage ?? "\(shortName) live login does not match the selected account",
        actionTitle: "Hide",
        destination: .apps
      )
    )
  }
  // Apps with no chosen route are setup work, not silent Ready.
  for app in model.apps where !isAppRouteConfigured(app) {
    if items.contains(where: { $0.id == "app.\(app.id)" }) { continue }
    items.append(
      TrayAttentionItem(
        id: "app.\(app.id)",
        title: "\(app.name) needs a login chosen",
        actionTitle: "Hide",
        destination: .apps
      )
    )
  }
  if model.routingConflicts.contains(where: \.needsAttention) {
    items.append(
      TrayAttentionItem(
        id: "hub.models",
        title: "Proxy Hub model conflict",
        actionTitle: "Hide",
        destination: .proxyHub
      )
    )
  }
  if model.trayScene == .attention, items.isEmpty {
    items = [
      TrayAttentionItem(
        id: "app.kiro",
        title: "Kiro live login does not match the selected account",
        actionTitle: "Hide",
        destination: .apps
      )
    ]
  }
  return items
}
