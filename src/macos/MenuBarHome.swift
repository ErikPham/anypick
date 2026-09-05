import SwiftUI

/// Attention notice as a native row: warning symbol, wrapping title,
/// and a Hide action. No custom card fill so keyboard focus stays visible.
struct MenuBarAttentionNotice: View {
  let item: TrayAttentionItem
  var extraCount = 0
  var onDismiss: () -> Void

  var body: some View {
    HStack(spacing: 8) {
      Image(systemName: "exclamationmark.triangle.fill")
        .symbolRenderingMode(.multicolor)
        .accessibilityHidden(true)
      Text(noticeTitle)
        .font(.callout)
        .frame(maxWidth: .infinity, alignment: .leading)
        .multilineTextAlignment(.leading)
        .fixedSize(horizontal: false, vertical: true)
      Button(item.actionTitle, action: onDismiss)
        .buttonStyle(.borderless)
        .accessibilityIdentifier("tray.fix.\(item.id).hide")
    }
    .padding(.vertical, 6)
    .accessibilityElement(children: .combine)
    .accessibilityLabel("\(noticeTitle). \(item.actionTitle)")
    .accessibilityIdentifier("tray.fix.\(item.id)")
    Divider()
  }

  private var noticeTitle: String {
    extraCount > 0 ? "\(item.title) · +\(extraCount) more" : item.title
  }
}

/// One app as native rows: header plus a Menu-based account picker.
/// Long names wrap to two lines instead of truncating to chips.
struct MenuBarAppCard: View {
  @ObservedObject var model: MacOSDesignModel
  let app: DesignApp
  var compact = false
  var onSwitch: () -> Void

  private var options: [DesignRoute] { compatibleAccountRoutes(in: app) }

  var body: some View {
    VStack(alignment: .leading, spacing: 4) {
      HStack(alignment: .center, spacing: 8) {
        DesignProviderIcon(provider: app.id, fallbackSymbol: app.symbol, size: compact ? 24 : 28)
          .accessibilityHidden(true)
        VStack(alignment: .leading, spacing: 1) {
          Text(app.name)
            .font(.headline)
            .lineLimit(1)
          Text(usingText)
            .font(.caption)
            .foregroundStyle(.secondary)
            .lineLimit(2)
            .fixedSize(horizontal: false, vertical: true)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .combine)
        .accessibilityLabel("\(app.name), \(usingText), \(trailingStatus)")
        Spacer(minLength: 6)
        if model.isSwitching(app.id) {
          ProgressView()
            .controlSize(.small)
            .accessibilityLabel("Switching \(app.name)")
        } else {
          Text(trailingStatus)
            .font(.caption)
            .fontWeight(.semibold)
            .foregroundStyle(trailingColor)
            .lineLimit(1)
            .accessibilityHidden(true)
        }
      }
      HStack(spacing: 8) {
        Menu {
          ForEach(options) { route in
            Button {
              model.setRoute(appID: app.id, routeID: route.id)
            } label: {
              if route.id == app.selectedRouteID {
                Label(route.name, systemImage: "checkmark")
              } else {
                Text(route.name)
              }
            }
            .disabled(model.isBusy)
          }
        } label: {
          Text(currentAccountName)
            .font(.callout)
            .lineLimit(1)
            .truncationMode(.tail)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .menuStyle(.borderlessButton)
        .disabled(model.isBusy || options.isEmpty)
        .help("Choose the account \(app.name) uses")
        .accessibilityLabel("Choose the account \(app.name) uses, current \(currentAccountName)")
        .accessibilityIdentifier("tray.app.\(app.id).account-menu")
        Button("Switch", action: onSwitch)
          .buttonStyle(.borderless)
          .font(.callout)
          .fontWeight(.medium)
          .foregroundStyle(Color.accentColor)
          .accessibilityIdentifier("tray.app.\(app.id).switch")
      }
      if !compact, model.showUsage, app.usageRemaining != nil {
        ProgressView(value: usageFraction)
          .controlSize(.mini)
          .accessibilityLabel("\(app.name) usage \(app.usageRemaining ?? "")")
      }
    }
    .padding(.vertical, 6)
    .accessibilityIdentifier("tray.app.\(app.id)")
    Divider()
  }

  private var currentAccountName: String {
    options.first(where: { $0.id == app.selectedRouteID })?.name ?? "Choose account…"
  }

  private var usingText: String {
    if let name = options.first(where: { $0.id == app.selectedRouteID })?.name {
      let count = options.count
      if compact { return "Using \(name)" }
      return "Using \(name) · \(count) \(count == 1 ? "account" : "accounts")"
    }
    return "No login chosen"
  }

  private var trailingStatus: String {
    if !isAppRouteConfigured(app) { return "Not Configured" }
    switch app.status {
    case .attention, .unavailable:
      return app.status.rawValue
    case .ready, .running, .stopped:
      if model.showUsage, let usage = app.usageRemaining {
        return usage.replacingOccurrences(of: " remaining", with: " left")
      }
      return app.status == .stopped ? app.status.rawValue : "Ready"
    }
  }

  private var trailingColor: Color {
    if !isAppRouteConfigured(app) { return .orange }
    switch app.status {
    case .attention, .unavailable: return .orange
    case .ready, .running: return .green
    case .stopped: return .secondary
    }
  }

  private var usageFraction: Double {
    guard let usage = app.usageRemaining else { return 0.5 }
    let number = usage.split(separator: "%").first.flatMap { Double($0) } ?? 50
    return min(max(number / 100, 0), 1)
  }
}

/// Proxies as native Toggle rows. The expanded list scrolls in a bounded
/// region so Search/Open/Quit stay reachable with 20+ proxies.
struct MenuBarProxiesRow: View {
  @ObservedObject var model: MacOSDesignModel
  let runningCount: Int
  let stoppedCount: Int
  let attentionCount: Int
  var openProxies: () -> Void
  @State private var expanded = false

  private var orderedProxies: [DesignProxy] {
    let indexed = model.proxies
    return indexed.sorted { lhs, rhs in
      let lhsRank = proxyRank(lhs)
      let rhsRank = proxyRank(rhs)
      if lhsRank != rhsRank { return lhsRank < rhsRank }
      return lhs.name.localizedStandardCompare(rhs.name) == .orderedAscending
    }
  }

  private func proxyRank(_ proxy: DesignProxy) -> Int {
    if proxy.status == .attention || proxy.status == .unavailable { return 0 }
    if proxy.enabled { return 1 }
    return 2
  }

  private var visibleProxies: [DesignProxy] {
    Array(orderedProxies.prefix(6))
  }

  private var hiddenCount: Int { max(0, orderedProxies.count - visibleProxies.count) }

  var body: some View {
    VStack(alignment: .leading, spacing: 0) {
      HStack(spacing: 8) {
        Button {
          withAnimation(.easeInOut(duration: 0.16)) { expanded.toggle() }
        } label: {
          HStack(spacing: 8) {
            Image(systemName: "server.rack")
              .foregroundStyle(.secondary)
              .frame(width: 14)
              .accessibilityHidden(true)
            Text("Proxies")
              .font(.callout)
            Spacer(minLength: 4)
            Text(summaryLabel)
              .font(.caption)
              .foregroundStyle(attentionCount > 0 ? .orange : .secondary)
              .lineLimit(1)
            Image(systemName: "chevron.right")
              .font(.caption)
              .fontWeight(.semibold)
              .foregroundStyle(.tertiary)
              .rotationEffect(.degrees(expanded ? 90 : 0))
              .accessibilityHidden(true)
          }
          .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(
          "Proxies, \(totalRunningCount) running, \(totalStoppedCount) stopped, \(attentionCount) need attention"
        )
        .accessibilityValue(expanded ? "Expanded" : "Collapsed")
        .accessibilityIdentifier("tray.row.proxies")
        Spacer(minLength: 4)
        Button("Details", action: openProxies)
          .buttonStyle(.borderless)
          .font(.callout)
      }
      .frame(height: 34)
      if expanded {
        Divider().padding(.leading, 22)
        hubProxyRow
        Divider().padding(.leading, 22)
        ScrollView {
          LazyVStack(spacing: 0) {
            ForEach(visibleProxies) { proxy in
              localProxyRow(proxy)
              Divider().padding(.leading, 22)
            }
          }
        }
        .frame(maxHeight: 220)
        if hiddenCount > 0 {
          Button("Show All Proxies… (\(orderedProxies.count))", action: openProxies)
            .buttonStyle(.borderless)
            .font(.callout)
            .padding(.vertical, 4)
            .accessibilityIdentifier("tray.proxies.show-all")
        }
      }
    }
  }

  private var summaryLabel: String {
    if attentionCount > 0 {
      return "\(attentionCount) issue · \(totalRunningCount) running"
    }
    return "\(totalRunningCount) running · \(totalStoppedCount) stopped"
  }

  private var totalRunningCount: Int { runningCount + (model.proxyHubEnabled ? 1 : 0) }

  private var totalStoppedCount: Int { stoppedCount + (model.proxyHubEnabled ? 0 : 1) }

  private var hubProxyRow: some View {
    HStack(spacing: 8) {
      Image(systemName: "point.3.connected.trianglepath.dotted")
        .foregroundStyle(.secondary)
        .frame(width: 18)
        .accessibilityHidden(true)
      VStack(alignment: .leading, spacing: 1) {
        Text("Proxy Hub")
          .font(.callout)
        Text("\(model.hubSourceIDs.count) sources · \(model.publishedModels.count) models")
          .font(.caption)
          .foregroundStyle(.secondary)
          .lineLimit(1)
      }
      .frame(maxWidth: .infinity, alignment: .leading)
      Spacer(minLength: 4)
      if model.proxyHubEnabled {
        Button {
          model.restartProxy(id: "hub")
        } label: {
          Image(systemName: "arrow.clockwise")
        }
        .buttonStyle(.borderless)
        .controlSize(.small)
        .help("Restart Proxy Hub")
        .accessibilityLabel("Restart Proxy Hub")
        .accessibilityIdentifier("tray.hub.restart")
      }
      Toggle("Proxy Hub", isOn: hubEnabled)
        .labelsHidden()
        .toggleStyle(.switch)
        .controlSize(.small)
        .disabled(model.isBusy || model.hubSourceIDs.isEmpty)
        .help(model.proxyHubEnabled ? "Stop Proxy Hub" : "Start Proxy Hub")
        .accessibilityIdentifier("tray.hub.toggle")
    }
    .frame(minHeight: 40)
  }

  private var hubEnabled: Binding<Bool> {
    Binding(
      get: { model.proxyHubEnabled },
      set: { model.setProxyHubEnabled($0) }
    )
  }

  private func localProxyRow(_ proxy: DesignProxy) -> some View {
    HStack(spacing: 8) {
      DesignProviderIcon(provider: proxy.provider, fallbackSymbol: proxy.symbol, size: 18)
        .accessibilityHidden(true)
      VStack(alignment: .leading, spacing: 1) {
        Text(proxy.name)
          .font(.callout)
          .lineLimit(1)
          .truncationMode(.tail)
        Text(proxy.detail)
          .font(.caption)
          .foregroundStyle(.secondary)
          .lineLimit(1)
          .truncationMode(.tail)
      }
      .frame(maxWidth: .infinity, alignment: .leading)
      .accessibilityElement(children: .combine)
      .accessibilityLabel("\(proxy.name), \(proxy.detail), \(proxy.enabled ? "running" : "stopped")")
      Spacer(minLength: 4)
      if proxy.enabled {
        Button {
          model.restartProxy(id: proxy.id)
        } label: {
          Image(systemName: "arrow.clockwise")
        }
        .buttonStyle(.borderless)
        .controlSize(.small)
        .help("Restart \(proxy.name)")
        .accessibilityLabel("Restart \(proxy.name)")
        .accessibilityIdentifier("tray.proxy.\(proxy.id).restart")
      }
      Toggle(proxy.name, isOn: proxyEnabled(proxy))
        .labelsHidden()
        .toggleStyle(.switch)
        .controlSize(.small)
        .disabled(model.isBusy)
        .help(proxy.enabled ? "Stop \(proxy.name)" : "Start \(proxy.name)")
        .accessibilityIdentifier("tray.proxy.\(proxy.id).toggle")
    }
    .frame(minHeight: 40)
  }

  private func proxyEnabled(_ proxy: DesignProxy) -> Binding<Bool> {
    Binding(
      get: { model.proxies.first(where: { $0.id == proxy.id })?.enabled ?? false },
      set: { model.setProxyEnabled(id: proxy.id, enabled: $0) }
    )
  }
}

struct MenuBarOtherAppsRow: View {
  let apps: [DesignApp]
  var showMarks = false
  var openApps: () -> Void

  var body: some View {
    Button(action: openApps) {
      HStack(spacing: 8) {
        Image(systemName: "square.grid.2x2")
          .foregroundStyle(.secondary)
          .frame(width: 14)
          .accessibilityHidden(true)
        Text("Other Apps")
          .font(.callout)
        Spacer(minLength: 4)
        Text(
          showMarks
            ? "\(apps.count)"
            : apps.map {
              $0.name.replacingOccurrences(of: " CLI", with: "")
            }.prefix(2).joined(separator: " · ")
        )
          .font(.caption)
          .foregroundStyle(.secondary)
          .lineLimit(1)
        Image(systemName: "chevron.right")
          .foregroundStyle(.tertiary)
          .accessibilityHidden(true)
      }
      .frame(height: 30)
    }
    .buttonStyle(.plain)
    .accessibilityIdentifier("tray.other-apps")
  }
}

struct MenuBarActivityRow: View {
  let title: String
  let time: String
  var openActivity: () -> Void

  var body: some View {
    Button(action: openActivity) {
      HStack(spacing: 8) {
        Image(systemName: "clock.arrow.circlepath")
          .foregroundStyle(.secondary)
          .frame(width: 14)
          .accessibilityHidden(true)
        Text(title)
          .font(.callout)
          .lineLimit(1)
        Spacer(minLength: 4)
        Text(time)
          .font(.caption)
          .foregroundStyle(.secondary)
        Text("Activity")
          .font(.callout)
          .fontWeight(.medium)
          .foregroundStyle(Color.accentColor)
      }
      .frame(height: 28)
    }
    .buttonStyle(.plain)
    .accessibilityIdentifier("tray.activity")
  }
}
