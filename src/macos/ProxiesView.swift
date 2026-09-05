import SwiftUI

struct ProxiesView: View {
  @ObservedObject var model: MacOSDesignModel
  @State private var confirmStopAll = false
  @State private var pendingStop: DesignProxy?
  /// Row under the pointer. Trailing actions stay hidden until hover (native
  /// list behavior) but keep layout + accessibility when hidden.
  @State private var hoveredProxyID: String?

  var body: some View {
    DesignGroupedPane {
      Section {
        NavigationLink(value: DesignRouteID.proxyHub) {
          DesignIdentityRow(
            title: "Proxy Hub",
            detail: "One address · included accounts publish models"
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
      }

      Section {
        if model.proxies.isEmpty {
          ContentUnavailableView {
            Label("No Account Proxies", systemImage: "server.rack")
          } description: {
            Text(
              "Saved accounts that can proxy appear here even when stopped. Add one under Accounts & APIs."
            )
          } actions: {
            Button("Open Accounts & APIs") { model.destination = .accounts }
          }
        } else {
          ForEach(model.proxies) { proxy in
            NavigationLink(value: DesignRouteID.proxy(proxy.id)) {
              DesignIdentityRow(
                title: proxy.name,
                detail: model.serviceOperations[proxy.id]?.label ?? proxy.detail,
                issue: proxyIssue(proxy)
              ) {
                DesignProviderIcon(provider: proxy.provider, fallbackSymbol: proxy.symbol)
              } trailing: {
                lifecycleAction(proxy)
              }
            }
            .accessibilityIdentifier("proxies.row.\(proxy.id)")
            .onHover { hovering in
              hoveredProxyID = hovering ? proxy.id : (hoveredProxyID == proxy.id ? nil : hoveredProxyID)
            }
          }
        }
      } header: {
        Text("Account Proxies")
      } footer: {
        Text(
          "All endpoints stay on this Mac and require authentication. Tokens are never shown."
        )
      }
    } actions: {
      Button("Restart Running Proxies") { model.restartRunningProxies() }
        .disabled(model.isBusy || !hasRunningProxy)
        .accessibilityIdentifier("proxies.restart")
      Button("Stop All…", role: .destructive) { confirmStopAll = true }
        .disabled(model.isBusy || !hasRunningProxy)
        .accessibilityIdentifier("proxies.stop-all")
    }
    .accessibilityIdentifier("proxies.list")
    .toolbar {
      ToolbarItem(placement: .primaryAction) {
        Button("Restart Running") { model.restartRunningProxies() }
          .disabled(model.isBusy || !hasRunningProxy)
          .accessibilityIdentifier("proxies.toolbar.restart")
      }
      ToolbarItem {
        Button("Stop All…") { confirmStopAll = true }
          .disabled(model.isBusy || !hasRunningProxy)
          .accessibilityIdentifier("proxies.toolbar.stop-all")
      }
    }
    .confirmationDialog(
      "Stop All Proxies?",
      isPresented: $confirmStopAll,
      titleVisibility: .visible
    ) {
      Button("Stop All", role: .destructive) { model.stopAllProxies() }
      Button("Cancel", role: .cancel) {}
    } message: {
      Text("Apps using these proxies will not connect until their services restart.")
    }
    .confirmationDialog(
      "Stop \(pendingStop?.name ?? "proxy")?",
      isPresented: Binding(
        get: { pendingStop != nil },
        set: { if !$0 { pendingStop = nil } }
      ),
      titleVisibility: .visible
    ) {
      Button("Stop Proxy", role: .destructive) {
        if let proxy = pendingStop {
          model.setProxyEnabled(id: proxy.id, enabled: false)
        }
      }
      Button("Cancel", role: .cancel) {}
    } message: {
      Text("Apps using this proxy will not connect until it restarts.")
    }
  }

  @ViewBuilder
  private func lifecycleAction(_ proxy: DesignProxy) -> some View {
    // Explicit bordered style: inside a NavigationLink label a default-styled
    // Button has its tap swallowed into navigation, so Start/Stop silently
    // did nothing. Hidden until hover (native lists), but a row with an
    // in-flight operation or failure always shows its state.
    // HIG: while busy the button stays and spins inside it — it is never
    // swapped out for a bare spinner.
    let busyRow = model.serviceOperations[proxy.id] != nil
    let busy = model.isBusy || busyRow
    let visible =
      hoveredProxyID == proxy.id || busyRow
      || model.serviceFailures[proxy.id] != nil
    Group {
      if proxy.enabled {
        Button(role: .destructive) {
          pendingStop = proxy
        } label: {
          if busyRow { ProgressView().controlSize(.small) } else { Text("Stop") }
        }
        .disabled(busy)
        .accessibilityLabel(busyRow ? "Stopping \(proxy.name)" : "Stop \(proxy.name)")
      } else if proxy.status == .attention || proxy.status == .unavailable {
        Button {
          model.restartProxy(id: proxy.id)
        } label: {
          if busyRow { ProgressView().controlSize(.small) } else { Text("Restart") }
        }
        .disabled(busy)
        .accessibilityLabel(busyRow ? "Restarting \(proxy.name)" : "Restart \(proxy.name)")
      } else {
        Button {
          model.setProxyEnabled(id: proxy.id, enabled: true)
        } label: {
          if busyRow { ProgressView().controlSize(.small) } else { Text("Start") }
        }
        .disabled(busy)
        .accessibilityLabel(busyRow ? "Starting \(proxy.name)" : "Start \(proxy.name)")
      }
    }
    .buttonStyle(.bordered)
    .controlSize(.small)
    .opacity(visible ? 1 : 0)
    .allowsHitTesting(visible)
    .accessibilityIdentifier("proxies.row-action.\(proxy.id)")
  }

  private var hasRunningProxy: Bool {
    model.proxies.contains(where: \.enabled)
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
