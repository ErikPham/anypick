import SwiftUI

struct MenuBarAttentionNotice: View {
  let item: TrayAttentionItem
  var extraCount = 0
  var onDismiss: () -> Void

  var body: some View {
    Button(action: onDismiss) {
      HStack(spacing: 8) {
        ZStack {
          RoundedRectangle(cornerRadius: 7, style: .continuous)
            .fill(TrayPaint.attention.opacity(0.18))
            .frame(width: 24, height: 24)
          DesignWarningSymbol()
        }
        Text(noticeTitle)
          .font(TrayFont.detailMedium)
          .frame(maxWidth: .infinity, alignment: .leading)
          .multilineTextAlignment(.leading)
        Text(item.actionTitle)
          .font(TrayFont.detailStrong)
          .foregroundStyle(TrayPaint.attention)
        Image(systemName: "xmark")
          .font(.system(size: 11, weight: .semibold))
          .foregroundStyle(TrayPaint.attention)
      }
      .padding(.horizontal, 9)
      .frame(minHeight: 42)
    }
    .buttonStyle(.plain)
    .background(TrayPaint.attention.opacity(0.10), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
    .overlay(
      RoundedRectangle(cornerRadius: 10, style: .continuous)
        .strokeBorder(TrayPaint.attention.opacity(0.28), lineWidth: 0.5)
    )
    .accessibilityIdentifier("tray.fix.\(item.id)")
  }

  private var noticeTitle: String {
    extraCount > 0 ? "\(item.title) · +\(extraCount) more" : item.title
  }
}

struct MenuBarAppCard: View {
  @ObservedObject var model: MacOSDesignModel
  let app: DesignApp
  var compact = false
  var onSwitch: () -> Void

  var body: some View {
    VStack(alignment: .leading, spacing: compact ? 5 : 7) {
      header
      accountShortcuts
    }
    .padding(compact ? 9 : 10)
    .frame(maxWidth: .infinity, alignment: .leading)
    .frame(minHeight: compact ? 76 : 96, alignment: .top)
    .background(
      TrayPaint.cardFill,
      in: RoundedRectangle(cornerRadius: compact ? 10 : 11, style: .continuous)
    )
    .overlay(
      RoundedRectangle(cornerRadius: compact ? 10 : 11, style: .continuous)
        .strokeBorder(TrayPaint.hairline, lineWidth: 0.5)
    )
    .accessibilityIdentifier("tray.app.\(app.id)")
  }

  private var header: some View {
    HStack(alignment: .center, spacing: compact ? 8 : 9) {
      DesignProviderIcon(provider: app.id, fallbackSymbol: app.symbol, size: compact ? 27 : 30)
      VStack(alignment: .leading, spacing: 2) {
        Text(app.name)
          .font(compact ? TrayFont.bodyStrong : TrayFont.title)
        Text(usingText)
          .font(TrayFont.detail)
          .foregroundStyle(.secondary)
          .lineLimit(1)
      }
      Spacer(minLength: 6)
      if compact {
        Text(trailingStatus)
          .font(TrayFont.detailStrong)
        Image(systemName: "chevron.right")
          .font(.system(size: 13, weight: .medium))
          .foregroundStyle(.tertiary)
      } else if model.isSwitching(app.id) {
        VStack(alignment: .trailing, spacing: 2) {
          ProgressView().controlSize(.small)
          Text("Switching…")
            .font(TrayFont.detailMedium)
            .foregroundStyle(.secondary)
        }
      } else {
        VStack(alignment: .trailing, spacing: 2) {
          Text(trailingStatus)
            .font(TrayFont.bodyStrong)
          Button("Switch", action: onSwitch)
            .buttonStyle(.borderless)
            .font(TrayFont.detailMedium)
            .foregroundStyle(TrayPaint.accent)
            .accessibilityIdentifier("tray.app.\(app.id).switch")
        }
      }
    }
    .contentShape(Rectangle())
    .onTapGesture {
      if compact { onSwitch() }
    }
  }

  private var usingText: String {
    if let name = app.routeOptions.first(where: { $0.id == app.selectedRouteID })?.name {
      if compact { return "Using \(name)" }
      let count = max(app.accountCount, trayAccountRoutes(in: app).count)
      return "Using \(name) · \(count) \(count == 1 ? "account" : "accounts")"
    }
    return app.detail
  }

  private var trailingStatus: String {
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

  private var accountShortcuts: some View {
    let options = trayAccountRoutes(in: app)
    let selected = options.first { $0.id == app.selectedRouteID }
    let rest = options.filter { $0.id != selected?.id }
    let shown = Array(([selected].compactMap { $0 } + rest).prefix(compact ? 2 : 3))
    let more = max(0, max(app.accountCount, options.count) - shown.count)

    return VStack(alignment: .leading, spacing: 5) {
      HStack(spacing: 5) {
        ForEach(shown) { route in
          TrayAccountChip(
            title: route.name,
            selected: route.id == app.selectedRouteID,
            compact: compact
          ) {
            model.setRoute(appID: app.id, routeID: route.id)
          }
          .disabled(model.isBusy)
        }
        Spacer(minLength: 4)
        if more > 0 {
          Button(compact ? "Switch" : "All", action: onSwitch)
            .buttonStyle(.borderless)
            .font(TrayFont.detailStrong)
            .foregroundStyle(TrayPaint.accent)
            .accessibilityIdentifier("tray.app.\(app.id).more")
        }
      }
      if !compact, model.showUsage, app.usageRemaining != nil {
        ProgressView(value: usageFraction)
          .tint(TrayPaint.accent)
          .controlSize(.mini)
      }
    }
  }

  private var usageFraction: Double {
    guard let usage = app.usageRemaining else { return 0.5 }
    let number = usage.split(separator: "%").first.flatMap { Double($0) } ?? 50
    return min(max(number / 100, 0), 1)
  }

}

struct MenuBarProxiesRow: View {
  @ObservedObject var model: MacOSDesignModel
  let runningCount: Int
  let stoppedCount: Int
  let attentionCount: Int
  var openProxies: () -> Void
  @State private var expanded = false

  var body: some View {
    VStack(alignment: .leading, spacing: 0) {
      HStack(spacing: 8) {
        Button {
          withAnimation(.easeInOut(duration: 0.16)) { expanded.toggle() }
        } label: {
          HStack(spacing: 8) {
            Image(systemName: "server.rack")
              .font(.system(size: 12, weight: .medium))
              .foregroundStyle(.secondary)
              .frame(width: 14)
            Text("Proxies")
              .font(TrayFont.detailMedium)
            Spacer(minLength: 4)
            Text(summaryLabel)
              .font(TrayFont.detail)
              .foregroundStyle(attentionCount > 0 ? TrayPaint.attention : .secondary)
            Image(systemName: "chevron.right")
              .font(.system(size: 10, weight: .semibold))
              .foregroundStyle(.tertiary)
              .rotationEffect(.degrees(expanded ? 90 : 0))
          }
          .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(
          "Proxies, \(totalRunningCount) running, \(totalStoppedCount) stopped, \(attentionCount) need attention"
        )
        .accessibilityValue(expanded ? "Expanded" : "Collapsed")
        .accessibilityIdentifier("tray.row.proxies")
        Button("Details", action: openProxies)
          .buttonStyle(.borderless)
          .font(TrayFont.detailMedium)
      }
      .padding(.horizontal, 9)
      .frame(height: 34)
      if expanded {
        Divider().padding(.leading, 31)
        hubProxyRow
        if !model.proxies.isEmpty {
          Divider().padding(.leading, 31)
          ForEach(Array(model.proxies.enumerated()), id: \.element.id) { index, proxy in
            localProxyRow(proxy)
            if index < model.proxies.count - 1 { Divider().padding(.leading, 31) }
          }
        }
      }
    }
    .background(TrayPaint.cardFill, in: RoundedRectangle(cornerRadius: 9, style: .continuous))
    .overlay(
      RoundedRectangle(cornerRadius: 9, style: .continuous)
        .strokeBorder(TrayPaint.hairline, lineWidth: 0.5)
    )
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
        .font(.system(size: 13, weight: .medium))
        .foregroundStyle(.secondary)
        .frame(width: 18)
      VStack(alignment: .leading, spacing: 1) {
        Text("Proxy Hub")
          .font(TrayFont.detailMedium)
        Text("\(model.hubSourceIDs.count) sources · \(model.publishedModels.count) models")
          .font(TrayFont.detail)
          .foregroundStyle(.secondary)
          .lineLimit(1)
      }
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
    .padding(.horizontal, 9)
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
      VStack(alignment: .leading, spacing: 1) {
        Text(proxy.name)
          .font(TrayFont.detailMedium)
          .lineLimit(1)
        Text(proxy.detail)
          .font(TrayFont.detail)
          .foregroundStyle(.secondary)
          .lineLimit(1)
      }
      Spacer(minLength: 4)
      if canRestartProxy(isRunning: proxy.status == .running, status: proxy.status) {
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
        .help(proxy.status == .running ? "Stop \(proxy.name)" : "Start \(proxy.name)")
        .accessibilityIdentifier("tray.proxy.\(proxy.id).toggle")
    }
    .padding(.horizontal, 9)
    .frame(minHeight: 40)
  }

  private func proxyEnabled(_ proxy: DesignProxy) -> Binding<Bool> {
    Binding(
      // `enabled` is the saved auto-start flag. The switch is the process:
      // a stopped proxy that is still marked enabled must read as off, or
      // turning it off invokes the toggle and starts the process.
      get: { model.proxies.first(where: { $0.id == proxy.id })?.status == .running },
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
          .font(.system(size: 12, weight: .medium))
          .foregroundStyle(.secondary)
          .frame(width: 14)
        Text("Other Apps")
          .font(TrayFont.detailMedium)
        if showMarks {
          HStack(spacing: 5) {
            ForEach(apps.prefix(3)) { app in
              DesignProviderIcon(provider: app.id, fallbackSymbol: app.symbol, size: 16)
            }
            if apps.contains(where: { $0.status == .attention || $0.status == .unavailable }) {
              Circle().fill(TrayPaint.attention).frame(width: 6, height: 6)
            }
          }
        }
        Spacer(minLength: 4)
        Text(
          showMarks
            ? "\(apps.count)"
            : apps.map {
              $0.name.replacingOccurrences(of: " CLI", with: "")
            }.prefix(2).joined(separator: " · ")
        )
          .font(TrayFont.detail)
          .foregroundStyle(.secondary)
          .lineLimit(1)
        Image(systemName: "chevron.right")
          .font(.system(size: 12, weight: .medium))
          .foregroundStyle(.tertiary)
      }
      .padding(.horizontal, 4)
      .frame(height: showMarks ? 32 : 30)
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
          .font(.system(size: 12, weight: .medium))
          .foregroundStyle(.secondary)
          .frame(width: 14)
        Text(title)
          .font(TrayFont.detailMedium)
          .lineLimit(1)
        Spacer(minLength: 4)
        Text(time)
          .font(TrayFont.detail)
          .foregroundStyle(.secondary)
        Text("Activity")
          .font(TrayFont.detailMedium)
          .foregroundStyle(TrayPaint.accent)
      }
      .padding(.horizontal, 4)
      .frame(height: 28)
    }
    .buttonStyle(.plain)
    .accessibilityIdentifier("tray.activity")
  }
}
