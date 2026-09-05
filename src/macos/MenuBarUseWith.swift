import SwiftUI

/// In-panel account switcher. Instant-apply native rows with a Menu-free
/// list: each route is its own button with visible keyboard focus.
struct MenuBarUseWithSheet: View {
  @ObservedObject var model: MacOSDesignModel
  let appID: String
  let onDismiss: () -> Void
  let openDestination: (DesignDestination) -> Void
  let openMainWindow: () -> Void
  let quit: () -> Void

  @State private var query = ""
  @FocusState private var searchFocused: Bool

  private var app: DesignApp? { model.apps.first { $0.id == appID } }

  private var accountRoutes: [DesignRoute] {
    guard let app else { return [] }
    return compatibleAccountRoutes(in: app)
  }

  private var filteredAccountRoutes: [DesignRoute] {
    let trimmed = query.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !trimmed.isEmpty else { return accountRoutes }
    return accountRoutes.filter {
      $0.name.localizedCaseInsensitiveContains(trimmed)
        || $0.detail.localizedCaseInsensitiveContains(trimmed)
    }
  }

  private var gatewayRoutes: [DesignRoute] {
    guard let app else { return [] }
    return trayGatewayRoutes(in: app)
  }

  private var hubRoute: DesignRoute? {
    app?.routeOptions.first { $0.kind == .hub }
  }

  var body: some View {
    VStack(alignment: .leading, spacing: 8) {
      header
      searchField
      savedAccountsHeader
      accountGroup
      sharedHeader
      if let hubRoute {
        sharedHubRow(hubRoute)
      }
      if !gatewayRoutes.isEmpty {
        gatewaySummary
      }
      Divider()
      footer
    }
  }

  private var header: some View {
    HStack(spacing: 8) {
      Button(action: onDismiss) {
        Image(systemName: "chevron.left")
      }
      .buttonStyle(.bordered)
      .controlSize(.small)
      .accessibilityLabel("Back")
      if let app {
        DesignProviderIcon(provider: app.id, fallbackSymbol: app.symbol, size: 28)
          .accessibilityHidden(true)
        VStack(alignment: .leading, spacing: 1) {
          Text("Switch \(app.name) Account")
            .font(.headline)
            .lineLimit(1)
          Text(headerDetail(for: app))
            .font(.caption)
            .foregroundStyle(.secondary)
            .lineLimit(1)
        }
      }
      Spacer(minLength: 0)
    }
  }

  @ViewBuilder
  private var searchField: some View {
    HStack(spacing: 6) {
      Image(systemName: "magnifyingglass")
        .foregroundStyle(.secondary)
        .accessibilityHidden(true)
      TextField(searchPrompt, text: $query)
        .textFieldStyle(.plain)
        .font(.callout)
        .focused($searchFocused)
    }
    .padding(.horizontal, 8)
    .frame(height: 30)
    .overlay {
      Button("Find") { searchFocused = true }
        .keyboardShortcut("f", modifiers: .command)
        .opacity(0)
        .accessibilityHidden(true)
    }
    .accessibilityIdentifier("tray.use-with.search")
    Divider()
  }

  private var savedAccountsHeader: some View {
    traySectionTitle("Compatible Accounts · \(accountRoutes.count)")
  }

  @ViewBuilder
  private var accountGroup: some View {
    if filteredAccountRoutes.isEmpty {
      ContentUnavailableView(
        "No matching accounts",
        systemImage: "magnifyingglass",
        description: Text("Try another name or email address.")
      )
      .frame(maxWidth: .infinity, minHeight: 86)
    } else {
      ScrollView {
        LazyVStack(spacing: 0) {
          ForEach(Array(filteredAccountRoutes.enumerated()), id: \.element.id) { index, route in
            if index > 0 { Divider() }
            routeRow(route, trailing: accountTrailing(route))
          }
        }
      }
      .frame(maxHeight: 258)
    }
  }

  private func routeRow(_ route: DesignRoute, trailing: String) -> some View {
    let state = trayInlineSwitchState(model: model, app: app, route: route)
    return Button {
      select(route)
    } label: {
      HStack(spacing: 8) {
        TrayInlineRouteMark(state: state)
          .accessibilityHidden(true)
        VStack(alignment: .leading, spacing: 1) {
          Text(route.name)
            .font(.callout)
            .fontWeight(state == .current ? .semibold : .regular)
            .foregroundStyle(.primary)
            .lineLimit(1)
            .truncationMode(.tail)
          if !route.detail.isEmpty {
            Text(route.detail)
              .font(.caption)
              .foregroundStyle(.secondary)
              .lineLimit(1)
              .truncationMode(.tail)
          }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        Spacer(minLength: 6)
        routeStateLabel(state, fallback: trailing)
      }
      .padding(.vertical, 6)
      .contentShape(Rectangle())
    }
    .buttonStyle(.plain)
    .disabled(model.isBusy)
    .accessibilityLabel(routeAccessibilityLabel(route, state: state))
    .help("\(route.name) — \(route.detail)")
  }

  @ViewBuilder
  private func routeStateLabel(_ state: TrayInlineSwitchState, fallback: String) -> some View {
    switch state {
    case .current:
      Text(fallback.isEmpty ? "Current" : fallback)
        .font(.caption)
        .fontWeight(.semibold)
        .foregroundStyle(Color.accentColor)
    case .switching:
      HStack(spacing: 4) {
        ProgressView().controlSize(.small)
        Text("Switching…")
      }
      .font(.caption)
      .fontWeight(.semibold)
      .foregroundStyle(.secondary)
    case .retry:
      Label("Try again", systemImage: "exclamationmark.triangle.fill")
        .font(.caption)
        .fontWeight(.semibold)
        .foregroundStyle(.orange)
    case .available:
      if !fallback.isEmpty {
        Text(fallback)
          .font(.caption)
          .foregroundStyle(.secondary)
      }
    }
  }

  private var sharedHeader: some View {
    traySectionTitle("Or Use Shared")
  }

  private func sharedHubRow(_ route: DesignRoute) -> some View {
    Button {
      model.setRoute(appID: appID, routeID: route.id)
    } label: {
      HStack(spacing: 8) {
        Image(systemName: "point.3.connected.trianglepath.dotted")
          .foregroundStyle(Color.accentColor)
          .frame(width: 20)
          .accessibilityHidden(true)
        VStack(alignment: .leading, spacing: 1) {
          Text("Proxy Hub")
            .font(.callout)
            .fontWeight(.semibold)
          Text(hubDetail)
            .font(.caption)
            .foregroundStyle(.secondary)
        }
        Spacer(minLength: 6)
        Text(model.proxyHubEnabled ? "Running" : "Stopped")
          .font(.caption)
          .fontWeight(.semibold)
          .foregroundStyle(model.proxyHubEnabled ? .green : .secondary)
        Image(systemName: "chevron.right")
          .foregroundStyle(.tertiary)
          .accessibilityHidden(true)
      }
      .padding(.vertical, 6)
      .contentShape(Rectangle())
    }
    .buttonStyle(.plain)
    .disabled(model.isBusy || !model.proxyHubEnabled)
    .accessibilityIdentifier("tray.use-with.hub")
  }

  private var gatewaySummary: some View {
    Button { openDestination(.accounts) } label: {
      HStack(spacing: 8) {
        Image(systemName: "network")
          .foregroundStyle(Color.accentColor)
          .frame(width: 20)
          .accessibilityHidden(true)
        Text("API Gateways")
          .font(.callout)
          .fontWeight(.semibold)
        Spacer(minLength: 6)
        Text("\(gatewayRoutes.count)")
          .font(.caption)
          .foregroundStyle(.secondary)
        Image(systemName: "chevron.right")
          .foregroundStyle(.tertiary)
          .accessibilityHidden(true)
      }
      .padding(.vertical, 6)
      .contentShape(Rectangle())
    }
    .buttonStyle(.plain)
    .accessibilityIdentifier("tray.use-with.gateways")
  }

  private var footer: some View {
    HStack(spacing: 8) {
      Button(action: quit) {
        Text("Quit")
          .frame(maxWidth: .infinity)
      }
      .buttonStyle(.bordered)
      .controlSize(.small)
      .keyboardShortcut("q", modifiers: .command)
      Button(action: openMainWindow) {
        Text("Open AnyPick")
          .frame(maxWidth: .infinity)
      }
      .buttonStyle(.borderedProminent)
      .controlSize(.small)
      .keyboardShortcut("o", modifiers: .command)
    }
  }

  private var searchPrompt: String {
    let name = app?.name
      .replacingOccurrences(of: " Code", with: "")
      .replacingOccurrences(of: " CLI", with: "") ?? "app"
    return "Search \(accountRoutes.count) compatible \(name) accounts"
  }

  private var hubDetail: String {
    let sources = model.hubSourceIDs.count
    let models = model.publishedModels.count
    return "\(sources) \(sources == 1 ? "source" : "sources") · \(models) \(models == 1 ? "model" : "models")"
  }

  private func headerDetail(for app: DesignApp) -> String {
    let current = app.routeOptions.first { $0.id == app.selectedRouteID }?.name ?? "Not selected"
    return "Current: \(current) · \(accountRoutes.count) compatible accounts"
  }

  private func routeAccessibilityLabel(_ route: DesignRoute, state: TrayInlineSwitchState) -> String {
    let provider = route.providerID.map(nativeProviderName) ?? ""
    let identity = [route.name, provider, route.detail]
      .map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }
      .filter { !$0.isEmpty }
      .joined(separator: ", ")
    switch state {
    case .current:
      return "\(identity), current account"
    case .switching:
      return "\(identity), switching account"
    case .retry:
      return "\(identity), switch failed, try again"
    case .available:
      return "Use \(identity)"
    }
  }

  private func accountTrailing(_ route: DesignRoute) -> String {
    guard let app else { return route.badge ?? "" }
    if route.id == app.selectedRouteID {
      if let badge = route.badge { return "Current · \(badge)" }
      if model.showUsage, let usage = app.usageRemaining {
        let short = usage
          .replacingOccurrences(of: " remaining", with: "")
          .replacingOccurrences(of: " left", with: "")
        return "Current · \(short)"
      }
      return "Current"
    }
    return route.badge ?? ""
  }

  private func select(_ route: DesignRoute) {
    model.setRoute(appID: appID, routeID: route.id)
  }
}
