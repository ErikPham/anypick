import AppKit
import SwiftUI

/// Power Quick Panel (board 29 / `d8hjQE`) — Ready, Attention, Use With, First Run.
struct MenuBarMenu: View {
  @ObservedObject var model: MacOSDesignModel
  var openMainWindow: (() -> Void)? = nil
  @Environment(\.openWindow) private var openWindow
  @State private var jump = ""
  @State private var useWithAppID: String?
  @State private var dismissedAttentionKeys = AppPreferences.dismissedAttentionKeys
  @FocusState private var jumpFocused: Bool

  private var primaryApps: [DesignApp] {
    trayPrimaryApps(from: model.quickPanelApps)
  }

  private var otherApps: [DesignApp] {
    trayOtherApps(from: model.quickPanelApps)
  }

  private var visibleAttentionItems: [TrayAttentionItem] {
    trayAttentionItems(from: model).filter { !dismissedAttentionKeys.contains(attentionDismissKey($0)) }
  }

  private var attentionNotice: TrayAttentionItem? {
    visibleAttentionItems.first
  }

  private var scene: DesignTrayScene {
    if useWithAppID != nil { return .useWith }
    switch model.trayScene {
    case .auto:
      if model.apps.isEmpty && model.accounts.isEmpty { return .firstRun }
      return trayAttentionItems(from: model).isEmpty ? .ready : .attention
    default:
      return model.trayScene
    }
  }

  private var needsAttention: Bool { scene == .attention || hasUnconfiguredApp }

  private var hasUnconfiguredApp: Bool {
    model.quickPanelApps.contains { !isAppRouteConfigured($0) }
      || model.apps.contains { !isAppRouteConfigured($0) }
  }

  private var reviewCount: Int {
    visibleAttentionItems.count
  }

  private var runningProxyCount: Int { model.proxies.filter { $0.status == .running }.count }

  private var stoppedProxyCount: Int { model.proxies.filter { $0.status == .stopped }.count }

  private var attentionProxyCount: Int {
    model.proxies.filter { $0.status == .attention || $0.status == .unavailable }.count
  }

  private var searchQuery: String {
    jump.trimmingCharacters(in: .whitespacesAndNewlines)
  }

  private var searchResults: [TraySearchResult] {
    let query = searchQuery
    guard !query.isEmpty else { return [] }

    var results = model.quickPanelApps.compactMap { app -> TraySearchResult? in
      let routeTerms = app.routeOptions.flatMap { [$0.name, $0.detail] }
      guard matchesSearch([app.name, app.detail] + routeTerms, query: query) else { return nil }
      return TraySearchResult(
        id: "app:\(app.id)",
        title: app.name,
        detail: app.detail,
        symbol: app.symbol,
        provider: app.id,
        target: .app(app.id)
      )
    }

    results += model.accounts.compactMap { account -> TraySearchResult? in
      guard
        matchesSearch(
          [account.name, account.detail, account.provider, account.kind.rawValue],
          query: query
        )
      else { return nil }
      return TraySearchResult(
        id: "account:\(account.id)",
        title: account.name,
        detail: [account.provider, account.detail].filter { !$0.isEmpty }.joined(separator: " · "),
        symbol: account.symbol,
        provider: account.provider,
        target: .account(account.id)
      )
    }

    results += DesignDestination.allCases.compactMap { destination -> TraySearchResult? in
      guard matchesSearch([destination.title, destination.shortTitle], query: query) else {
        return nil
      }
      return TraySearchResult(
        id: "destination:\(destination.id)",
        title: destination.title,
        detail: "Open in AnyPick",
        symbol: destination.symbol,
        provider: nil,
        target: .destination(destination)
      )
    }

    return Array(results.prefix(12))
  }

  var body: some View {
    Group {
      switch scene {
      case .firstRun:
        firstRunBody
      case .useWith:
        if let appID = useWithAppID ?? model.trayUseWithAppID {
          MenuBarUseWithSheet(
            model: model,
            appID: appID,
            onDismiss: dismissUseWith,
            openDestination: open,
            openMainWindow: { open(nil) },
            quit: quit
          )
        }
      default:
        readyOrAttention
      }
    }
    .padding(scene == .firstRun ? 12 : 8)
    .frame(width: 336, alignment: .leading)
    .onAppear {
      if model.trayScene == .useWith {
        useWithAppID = model.trayUseWithAppID ?? "codex"
      }
    }
  }

  private var firstRunBody: some View {
    VStack(alignment: .leading, spacing: 9) {
      MenuBarFirstRun(model: model) { open(.accounts) }
      trayFooterHairline
      homeFooter()
    }
  }

  private var readyOrAttention: some View {
    VStack(alignment: .leading, spacing: 7) {
      searchField
      statusRow
      if let item = attentionNotice {
        MenuBarAttentionNotice(
          item: item,
          extraCount: max(reviewCount - 1, 0),
          onDismiss: { dismissAttentionNotice(item) }
        )
      }
      readyHome
      trayFooterHairline
      homeFooter()
    }
  }

  @ViewBuilder
  private var readyHome: some View {
    if !searchQuery.isEmpty {
      searchHome
    } else {
      if !primaryApps.isEmpty {
        ForEach(primaryApps) { app in
          MenuBarAppCard(model: model, app: app, compact: false) { openUseWith(app.id) }
        }
      }
      if !otherApps.isEmpty {
        MenuBarOtherAppsRow(apps: otherApps, showMarks: false) { open(.apps) }
      }
      routingServices
    }
  }

  @ViewBuilder
  private var searchHome: some View {
    MenuBarSearchResults(query: searchQuery, results: searchResults, activate: activateSearchResult)
  }

  private var routingServices: some View {
    VStack(alignment: .leading, spacing: 7) {
      TrayRoutingHeader(showDetails: false) {}
      MenuBarProxiesRow(
        model: model,
        runningCount: runningProxyCount,
        stoppedCount: stoppedProxyCount,
        attentionCount: attentionProxyCount
      ) { open(.proxies) }
    }
  }

  @ViewBuilder
  private var searchField: some View {
    HStack(spacing: 6) {
      Image(systemName: "magnifyingglass")
        .foregroundStyle(.secondary)
        .accessibilityHidden(true)
      TextField(searchPrompt, text: $jump)
        .textFieldStyle(.plain)
        .font(.callout)
        .focused($jumpFocused)
        .onSubmit(performJump)
      if jump.isEmpty {
        Text("⌘K")
          .font(.caption)
          .fontWeight(.semibold)
          .foregroundStyle(.secondary)
      } else {
        Button {
          jump = ""
          jumpFocused = true
        } label: {
          Image(systemName: "xmark.circle.fill")
            .foregroundStyle(.tertiary)
        }
        .buttonStyle(.plain)
        .help("Clear Search")
        .accessibilityLabel("Clear Search")
      }
    }
    .padding(.horizontal, 8)
    .frame(height: 30)
    .background {
      Button("Focus Search") { jumpFocused = true }
        .keyboardShortcut("k", modifiers: .command)
        .frame(width: 1, height: 1)
        .opacity(0)
        .allowsHitTesting(false)
        .accessibilityHidden(true)
    }
    .accessibilityIdentifier("tray.jump")
    Divider()
  }

  private var searchPrompt: String {
    let count = model.accounts.filter { $0.kind == .saved }.count
    return "Search \(count) saved \(count == 1 ? "account" : "accounts")…"
  }

  private var statusRow: some View {
    HStack(spacing: 6) {
      Circle()
        .fill(statusColor)
        .frame(width: 7, height: 7)
        .accessibilityHidden(true)
      Text(statusTitle)
        .font(.callout)
        .fontWeight(.semibold)
        .foregroundStyle(statusColor)
      Text(statusDetail)
        .font(.caption)
        .foregroundStyle(.secondary)
        .lineLimit(1)
      Spacer(minLength: 6)
      Button(action: model.refresh) {
        Image(systemName: "arrow.clockwise")
          .foregroundStyle(.secondary)
      }
      .buttonStyle(.borderless)
      .help("Refresh")
      .accessibilityLabel("Refresh")
    }
    .frame(height: needsAttention ? 28 : 24)
    .accessibilityElement(children: .combine)
    .accessibilityLabel("Status: \(statusTitle), \(statusDetail)")
  }

  private var statusColor: Color {
    scene == .attention ? .orange : (hasUnconfiguredApp ? .orange : .green)
  }

  private var statusTitle: String {
    if scene == .attention { return "Needs attention" }
    if hasUnconfiguredApp { return "Not Configured" }
    return "Ready"
  }

  private var statusDetail: String {
    if scene == .attention {
      return "\(reviewCount) \(reviewCount == 1 ? "issue" : "issues")"
    }
    if hasUnconfiguredApp {
      let missing = model.apps.filter { !isAppRouteConfigured($0) }.count
      return "Choose a login for \(missing) \(missing == 1 ? "app" : "apps")"
    }
    let appCount = model.quickPanelApps.count
    if model.proxyHubEnabled {
      return "Hub online · \(runningProxyCount) proxies · \(appCount) apps"
    }
    if runningProxyCount > 0 { return "Proxies running · \(appCount) apps" }
    return "\(appCount) apps"
  }

  private var trayFooterHairline: some View {
    Rectangle()
      .fill(Color.primary.opacity(0.10))
      .frame(height: 1)
      .padding(.top, 1)
  }

  private func homeFooter() -> some View {
    HStack(spacing: 8) {
      Button(action: quit) {
        Text("Quit")
          .frame(maxWidth: .infinity)
      }
        .buttonStyle(.bordered)
        .controlSize(.small)
        .keyboardShortcut("q", modifiers: .command)
        .accessibilityIdentifier("menu.quit")
      Button {
        open(nil)
      } label: {
        Text("Open AnyPick")
          .frame(maxWidth: .infinity)
      }
        .buttonStyle(.borderedProminent)
        .controlSize(.small)
        .keyboardShortcut("o", modifiers: .command)
        .accessibilityIdentifier("menu.open")
    }
    .frame(height: 28)
  }

  private func attentionDismissKey(_ item: TrayAttentionItem) -> String {
    "\(item.id)|\(item.title)"
  }

  private func dismissAttentionNotice(_ item: TrayAttentionItem) {
    dismissedAttentionKeys.insert(attentionDismissKey(item))
    AppPreferences.dismissedAttentionKeys = dismissedAttentionKeys
  }

  private func openUseWith(_ appID: String) {
    useWithAppID = appID
  }

  private func dismissUseWith() {
    useWithAppID = nil
    if model.trayScene == .useWith {
      model.trayScene = .ready
    }
  }

  private func performJump() {
    guard !searchQuery.isEmpty else {
      jumpFocused = true
      return
    }
    guard let result = searchResults.first else {
      NSSound.beep()
      return
    }
    activateSearchResult(result)
  }

  private func matchesSearch(_ values: [String], query: String) -> Bool {
    values.contains { $0.localizedCaseInsensitiveContains(query) }
  }

  private func activateSearchResult(_ result: TraySearchResult) {
    jump = ""
    switch result.target {
    case .app(let appID):
      openUseWith(appID)
    case .account(let accountID):
      // The main window owns the NavigationStack; hand it the account id
      // and let it push the detail once the Accounts pane is visible.
      model.pendingAccountDetailID = accountID
      open(.accounts)
    case .destination(let destination):
      open(destination)
    }
  }

  private func open(_ destination: DesignDestination?) {
    if let destination { model.destination = destination }
    if let openMainWindow { openMainWindow() }
    else { openWindow(id: "main") }
    NSApp.setActivationPolicy(.regular)
    NSApp.activate(ignoringOtherApps: true)
  }

  private func open(_ destination: DesignDestination) {
    open(Optional(destination))
  }

  private func quit() {
    guard model.proxyHubEnabled || model.proxies.contains(where: \.enabled) else {
      AnyPickAppDelegate.requestQuit()
      return
    }
    let alert = NSAlert()
    alert.messageText = "Quit AnyPick?"
    alert.informativeText = "Local proxies will stop."
    alert.alertStyle = .warning
    alert.addButton(withTitle: "Quit")
    alert.addButton(withTitle: "Cancel")
    if alert.runModal() == .alertFirstButtonReturn {
      AnyPickAppDelegate.requestQuit()
    }
  }
}
