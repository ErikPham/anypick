import AppKit
import SwiftUI

struct ProxiesView: View {
  @ObservedObject var model: MacOSDesignModel
  @State private var confirmation: DesignConfirmation?
  @State private var searchQuery = ""
  @State private var startAllInProgress = false
  @State private var startAllTitle = "Start All Proxies"

  private var filteredProxies: [DesignProxy] {
    let query = searchQuery.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !query.isEmpty else { return model.proxies }
    return model.proxies.filter { proxy in
      [
        proxy.name,
        proxyDisplayName(for: proxy),
        proxy.detail,
        proxy.provider,
        proxy.status.rawValue,
        anypickDisplayName(proxy.provider)
      ].contains { $0.localizedCaseInsensitiveContains(query) }
    }
  }

  var body: some View {
    Group {
      // An empty collection replaces the pane, the way AccountsView does it:
      // ContentUnavailableView is the system's empty state and fills the
      // content area. Nesting it inside a Form Section turned it into a
      // card-sized hero — a grouped Form paints each Section as an inset
      // group, so the system empty state grew a container it was never meant
      // to have.
      if model.proxies.isEmpty {
        DesignEmptyState(
          title: "No Account Proxies",
          message:
            "Saved accounts that can proxy appear here even when stopped. Add one under Accounts & APIs.",
          symbol: "server.rack",
          actionTitle: "Open Accounts & APIs",
          action: { model.destination = .accounts }
        )
      } else if filteredProxies.isEmpty {
        DesignEmptyState(
          title: "No Results",
          message: "No proxies match “\(searchQuery)”.",
          symbol: "magnifyingglass",
          actionTitle: "Clear Search",
          action: { searchQuery = "" }
        )
      } else {
        inventory
      }
    }
    .toolbar {
      ToolbarItem(placement: .primaryAction) {
        DesignToolbarSearch(text: $searchQuery, prompt: "Search Proxies")
          .frame(minWidth: 160, idealWidth: 180, maxWidth: 220)
      }
    }
    .designConfirmation($confirmation)
  }

  private func requestStopAll() {
    confirmation = DesignConfirmation(
      id: "stop-all-proxies",
      title: "Stop All Proxies?",
      message: "Apps using these proxies will not connect until their services restart.",
      action: .confirm("Stop All") { model.stopAllProxies() }
    )
  }

  private func requestStop(_ proxy: DesignProxy) {
    confirmation = DesignConfirmation(
      id: "stop-proxy",
      title: "Stop \(proxy.name)?",
      message: "Apps using this proxy will not connect until it restarts.",
      action: .confirm("Stop Proxy") { model.setProxyEnabled(id: proxy.id, enabled: false) }
    )
  }

  private func requestRotate(_ proxy: DesignProxy) {
    confirmation = DesignConfirmation(
      id: "rotate-proxy-key",
      title: "Rotate API Key for \(proxy.name)?",
      message:
        "Apps using the current API key will be disconnected until you update them with the new key.",
      action: .confirm("Rotate Key") { model.rotateProxyKey(id: proxy.id) }
    )
  }

  /// The pane's one job: the Proxy Hub shortcut, then one row per account proxy.
  private var inventory: some View {
    DesignGroupedPane(contentIdentifier: "proxies.list", actions: { paneActions }) {
      Section {
        NavigationLink(value: DesignRouteID.proxyHub) {
          DesignIdentityRow(
            title: "Proxy Hub",
            detail: hubSummarySubtitle
          ) {
            Image(systemName: DesignDestination.proxyHub.symbol)
              .foregroundStyle(.secondary)
          } trailing: {
            DesignStatusLabel(status: model.proxyHubEnabled ? .running : .stopped)
          }
        }
        .accessibilityIdentifier("proxies.hub-summary")
      } header: {
        Text("Proxy Hub")
      } footer: {
        Text("A unified local endpoint combining published models across your accounts.")
      }

      Section {
        ForEach(filteredProxies) { proxy in
          let title = proxyDisplayName(for: proxy)
          let subtitle = proxySubtitle(for: proxy, title: title)
          NavigationLink(value: DesignRouteID.proxy(proxy.id)) {
            DesignIdentityRow(
              title: title,
              detail: subtitle,
              issue: proxyIssue(proxy)
            ) {
              DesignProviderIcon(provider: proxy.provider, fallbackSymbol: proxy.symbol)
            } trailing: {
              lifecycleAction(proxy)
            }
          }
          .accessibilityIdentifier("proxies.row.\(proxy.id)")
          .contextMenu {
            if proxy.status == .running {
              Button("Stop", role: .destructive) { requestStop(proxy) }
                .help("Stop this local proxy. Apps using it will lose this connection.")
                .disabled(
                  model.isBusy || startAllInProgress || model.serviceOperations[proxy.id] != nil
                )
              Button("Restart") { model.restartProxy(id: proxy.id) }
                .help("Restart this local proxy. Active connections may be interrupted briefly.")
                .disabled(
                  model.isBusy || startAllInProgress || model.serviceOperations[proxy.id] != nil
                )
              if proxy.port > 0 {
                Button("Copy Address") {
                  let address = "http://127.0.0.1:\(proxy.port)"
                  if model.pasteString(address) {
                    model.announceOnly("Copied \(proxy.name) address.")
                  } else {
                    model.announceOnly("Couldn’t copy \(proxy.name) address.")
                  }
                }
                .help("Copy this proxy’s local address.")
              }
              if model.proxyCanCopyAPIKey(proxy) {
                Button("Copy API Key") { model.copyProxyAPIKey(id: proxy.id) }
                  .help("Copy the proxy API key to the clipboard without displaying it.")
              }
              Button("Rotate API Key…") {
                requestRotate(proxy)
              }
              .help("Replace this proxy’s API key. Existing clients must use the new key.")
            } else {
              Button("Start") { model.setProxyEnabled(id: proxy.id, enabled: true) }
                .help("Start this local proxy for compatible apps.")
                .disabled(
                  model.isBusy || startAllInProgress || model.serviceOperations[proxy.id] != nil
                )
            }
          }
          .accessibilityAction(named: proxy.status == .running ? "Stop" : "Start") {
            guard !startAllInProgress else { return }
            if proxy.status == .running {
              requestStop(proxy)
            } else {
              model.setProxyEnabled(id: proxy.id, enabled: true)
            }
          }
        }
      } header: {
        Text("Account Proxies")
      } footer: {
        Text(
          "All endpoints stay on this Mac and require authentication. Select a proxy to view its address and API key."
        )
      }
    }
  }

  private var paneActions: some View {
    DesignPaneActions {
      if hasProxiesToStart || startAllInProgress {
        DesignSheetButton(
          title: startAllInProgress
            ? startAllTitle
            : (hasRunningProxy ? "Start Remaining Proxies" : "Start All Proxies"),
          enabled: !model.isBusy && !startAllInProgress && !hasPendingProxyOperation,
          identifier: "proxies.start-all"
        ) {
          startAllProxies()
        }
      }
      DesignSheetButton(
        title: "Restart Running Proxies",
        enabled: !model.isBusy && !startAllInProgress && hasRunningProxy,
        identifier: "proxies.restart"
      ) { model.restartRunningProxies() }
      DesignSheetButton(
        title: "Stop All…",
        enabled: !model.isBusy && !startAllInProgress && hasRunningProxy,
        destructive: true,
        identifier: "proxies.stop-all"
      ) { requestStopAll() }
    }
  }

  @ViewBuilder
  private func lifecycleAction(_ proxy: DesignProxy) -> some View {
    // Explicit bordered style: inside a NavigationLink label a default-styled
    // Button has its tap swallowed into navigation, so Start/Stop silently
    // did nothing. Keep the control visible so lifecycle actions remain
    // discoverable with a pointer, keyboard, or assistive technology.
    // HIG: while busy the button stays and spins inside it — it is never
    // swapped out for a bare spinner.
    let busyRow = model.serviceOperations[proxy.id] != nil
    let busy = model.isBusy || busyRow || (startAllInProgress && !busyRow)
    Group {
      // `enabled` is the persisted auto-start/configuration flag. It is not
      // the lifecycle state of the process, so it must never choose Stop.
      if proxy.status == .running {
        Button(role: .destructive) {
          requestStop(proxy)
        } label: {
          ZStack {
            if busyRow { ProgressView().controlSize(.small) } else { Text("Stop") }
          }
          .frame(minWidth: 46)
        }
        .disabled(busy)
        .help("Stop this local proxy. Apps using it will lose this connection.")
        .accessibilityLabel(busyRow ? "Stopping \(proxy.name)" : "Stop \(proxy.name)")
      } else {
        Button {
          model.setProxyEnabled(id: proxy.id, enabled: true)
        } label: {
          ZStack {
            if busyRow { ProgressView().controlSize(.small) } else { Text("Start") }
          }
          .frame(minWidth: 46)
        }
        .disabled(busy)
        .help("Start this local proxy for compatible apps.")
        .accessibilityLabel(busyRow ? "Starting \(proxy.name)" : "Start \(proxy.name)")
      }
    }
    .buttonStyle(.bordered)
    .controlSize(.small)
    .accessibilityIdentifier("proxies.row-action.\(proxy.id)")
  }

  private var hasRunningProxy: Bool {
    model.proxies.contains { $0.status == .running }
  }

  private var hasProxiesToStart: Bool {
    model.proxies.contains { $0.status != .running }
  }

  private var hasPendingProxyOperation: Bool {
    model.proxies.contains { model.serviceOperations[$0.id] != nil }
  }

  @MainActor
  private func startAllProxies() {
    guard !startAllInProgress, !model.isBusy, !hasPendingProxyOperation else { return }
    startAllTitle = hasRunningProxy ? "Start Remaining Proxies" : "Start All Proxies"
    startAllInProgress = true
    Task { @MainActor in
      defer { startAllInProgress = false }

      let proxyIDs = model.proxies.filter { $0.status != .running }.map(\.id)
      for proxyID in proxyIDs {
        while model.isBusy {
          try? await Task.sleep(nanoseconds: 50_000_000)
        }
        guard let proxy = model.proxies.first(where: { $0.id == proxyID }),
          proxy.status != .running
        else { continue }

        model.setProxyEnabled(id: proxyID, enabled: true)
        // Production installs an operation only after the service accepted the
        // request. A missing operation means stop the batch after surfacing the
        // rejection; otherwise later rows would produce duplicate alerts.
        guard model.serviceOperations[proxyID] != nil else { return }

        while model.isBusy || model.serviceOperations[proxyID] != nil {
          try? await Task.sleep(nanoseconds: 50_000_000)
        }
      }
    }
  }

  private var hubSummarySubtitle: String {
    if model.proxyHubEnabled {
      let modelCount = model.publishedModels.filter { !$0.needsChoice }.count
      return "http://127.0.0.1:\(model.proxyHubPort) · \(modelCount) \(modelCount == 1 ? "model" : "models") published"
    }
    return "One address · included accounts publish models"
  }

  private func proxyDisplayName(for proxy: DesignProxy) -> String {
    friendlyProxyName(label: proxy.name, providerId: proxy.provider)
  }

  private func proxySubtitle(for proxy: DesignProxy, title: String) -> String {
    if let op = model.serviceOperations[proxy.id]?.label {
      return op
    }
    let statusText = friendlyProxyStatusSubtitle(
      status: proxy.status,
      port: proxy.port,
      detail: proxy.detail
    )
    let providerName = anypickDisplayName(proxy.provider)
    let cleanDetail = proxy.detail.trimmingCharacters(in: .whitespacesAndNewlines)
    let isRawAddressOrStatus = cleanDetail.isEmpty
      || cleanDetail.hasPrefix("http://")
      || cleanDetail.hasPrefix("https://")
      || cleanDetail.hasPrefix("127.0.0.1")
      || cleanDetail.hasPrefix("[::1]")
      || cleanDetail.localizedCaseInsensitiveCompare("stopped") == .orderedSame
      || cleanDetail.localizedCaseInsensitiveCompare("running") == .orderedSame

    if !isRawAddressOrStatus {
      var accountPart = cleanDetail
      if let range = cleanDetail.range(of: " · ") {
        accountPart = String(cleanDetail[..<range.lowerBound]).trimmingCharacters(in: .whitespacesAndNewlines)
      }
      if !accountPart.isEmpty && !title.localizedCaseInsensitiveContains(accountPart) {
        return "\(accountPart) · \(statusText)"
      }
    }

    if !title.localizedCaseInsensitiveContains(providerName) {
      return "\(providerName) · \(statusText)"
    }
    return statusText
  }

  private func proxyIssue(_ proxy: DesignProxy) -> String? {
    if let failure = model.serviceFailures[proxy.id] { return failure }
    switch proxy.status {
    case .attention, .unavailable:
      return "This proxy isn’t running."
    case .ready, .running, .stopped:
      return nil
    }
  }
}
