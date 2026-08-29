import SwiftUI

struct ProxiesView: View {
  @ObservedObject var model: MacOSDesignModel
  @State private var confirmStopAll = false
  @State private var pendingStop: DesignProxy?

  var body: some View {
    Group {
      if model.proxies.isEmpty {
        DesignEmptyState(
          title: "No Proxies",
          message: "Add a compatible saved account to make its local translation service available.",
          symbol: "server.rack",
          actionTitle: "Open Accounts & APIs",
          action: { model.destination = .accounts }
        )
      } else {
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
      }
    }
    .accessibilityIdentifier("proxies.list")
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
    let busy = model.isBusy || model.serviceOperations[proxy.id] != nil
    if proxy.enabled {
      Button("Stop", role: .destructive) { pendingStop = proxy }
        .disabled(busy)
        .controlSize(.small)
        .accessibilityIdentifier("proxies.row-action.\(proxy.id)")
    } else if proxy.status == .attention || proxy.status == .unavailable {
      Button("Restart") { model.restartProxy(id: proxy.id) }
        .disabled(busy)
        .controlSize(.small)
        .accessibilityIdentifier("proxies.row-action.\(proxy.id)")
    } else {
      Button("Start") { model.setProxyEnabled(id: proxy.id, enabled: true) }
        .disabled(busy)
        .controlSize(.small)
        .accessibilityIdentifier("proxies.row-action.\(proxy.id)")
    }
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
