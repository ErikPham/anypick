import AppKit
import SwiftUI

struct ProxyDetailView: View {
  @ObservedObject var model: MacOSDesignModel
  let proxyID: String
  @State private var didCopy = false
  @State private var didCopyApiKey = false
  @State private var confirmStop = false
  @State private var confirmRotate = false

  private var proxy: DesignProxy? { model.proxies.first { $0.id == proxyID } }

  var body: some View {
    Group {
      if let proxy {
        DesignGroupedPane(
          actions: {
            proxyActions(for: proxy)
          }
        ) {
          Section {
            Label {
              VStack(alignment: .leading, spacing: 2) {
                Text(proxy.name)
                Text(purpose)
                  .foregroundStyle(.secondary)
              }
            } icon: {
              DesignProviderIcon(provider: proxy.provider, fallbackSymbol: proxy.symbol)
            }
            LabeledContent("Status") {
              HStack(spacing: 8) {
                if let operation = model.serviceOperations[proxy.id] {
                  HStack(spacing: 6) {
                    ProgressView()
                      .controlSize(.small)
                    Text(operation.label)
                      .foregroundStyle(.secondary)
                  }
                } else {
                  DesignStatusLabel(status: proxy.status)
                }
                proxyLifecycleAction(proxy)
              }
            }
            LabeledContent("Provider", value: providerName)
            if !servingAccountDisplay.isEmpty {
              LabeledContent("Serving Account", value: servingAccountDisplay)
            }
          } header: {
            Text("Service")
          }

          if let failure = model.serviceFailures[proxy.id] {
            Section {
              Label {
                VStack(alignment: .leading, spacing: 2) {
                  Text("Service Error")
                  Text(failure)
                    .foregroundStyle(.secondary)
                }
              } icon: {
                DesignWarningSymbol()
              }
            } footer: {
              Text("Adjust the service configuration or restart to clear this error.")
            }
          } else if proxy.status == .attention {
            Section {
              Label {
                VStack(alignment: .leading, spacing: 2) {
                  Text("Needs Attention")
                  Text("This proxy is enabled but not currently running.")
                    .foregroundStyle(.secondary)
                }
              } icon: {
                DesignWarningSymbol()
              }
            } footer: {
              Text("Click Start to launch this compatibility relay.")
            }
          }

          Section {
            LabeledContent("Address") {
              if proxy.port > 0 {
                HStack(spacing: 8) {
                  Text("http://127.0.0.1:\(proxy.port)")
                    .font(.system(.body, design: .monospaced))
                    .foregroundStyle(.secondary)
                    .textSelection(.enabled)
                  DesignCopyButton(
                    copied: didCopy,
                    label: "Copy proxy address",
                    identifier: "proxies.inspector.copy",
                    action: copyAddress
                  )
                }
              } else {
                Text("Not running")
                  .foregroundStyle(.secondary)
                  .accessibilityIdentifier("proxies.inspector.address")
              }
            }
            if proxy.port > 0 && model.proxyCanCopyAPIKey(proxy) {
              LabeledContent("API Key") {
                HStack(spacing: 8) {
                  if proxy.apiKey.isEmpty {
                    Text("Hidden until copied")
                      .foregroundStyle(.secondary)
                  } else {
                    Text(proxy.apiKey)
                      .font(.system(.body, design: .monospaced))
                      .foregroundStyle(.secondary)
                      .lineLimit(1)
                      .truncationMode(.middle)
                      .textSelection(.enabled)
                  }
                  DesignCopyButton(
                    copied: didCopyApiKey,
                    label: "Copy proxy API key",
                    identifier: "proxies.inspector.copy-key",
                    action: { model.copyProxyAPIKey(id: proxy.id) }
                  )
                  if proxy.status == .running {
                    Button {
                      confirmRotate = true
                    } label: {
                      Image(systemName: "arrow.triangle.2.circlepath")
                    }
                    .buttonStyle(.borderless)
                    .help("Rotate API Key")
                    .accessibilityLabel("Rotate API Key")
                    .accessibilityIdentifier("proxies.inspector.rotate-key")
                  }
                }
              }
            }
            if proxy.port > 0 {
              LabeledContent("Port", value: "\(proxy.port)")
            }
            LabeledContent("Security", value: "Loopback only (authenticated)")
            LabeledContent("Activity & Logs") {
              Button {
                model.destination = .activity
              } label: {
                HStack(spacing: 4) {
                  Text("View in Activity")
                  Image(systemName: "arrow.up.forward.app")
                    .font(.caption2)
                }
              }
              .buttonStyle(.plain)
              .foregroundStyle(Color.accentColor)
            }
          } header: {
            Text("Endpoint")
          } footer: {
            Text(model.proxyCanCopyAPIKey(proxy)
              ? "This proxy listens only on this Mac (127.0.0.1). Authenticate requests with its API key (Bearer token or x-api-key header)."
              : "This proxy listens only on this Mac (127.0.0.1).")
          }

          if !connectedApps.isEmpty {
            Section {
              ForEach(connectedApps) { app in
                NavigationLink(value: DesignRouteID.app(app.id)) {
                  Label {
                    Text(app.name)
                  } icon: {
                    Image(systemName: app.symbol)
                      .foregroundStyle(.secondary)
                  }
                }
              }
            } header: {
              Text("Client Apps")
            } footer: {
              Text("These apps are configured to route through this proxy relay.")
            }
          }
        }
      } else {
        DesignEmptyState(
          title: "Proxy Is No Longer Available",
          message: "Return to Proxies and choose another service.",
          symbol: "server.rack"
        )
      }
    }
    .navigationTitle(proxy?.name ?? "Proxy")
    .confirmationDialog(
      "Stop \(proxy?.name ?? "This Proxy")?",
      isPresented: $confirmStop,
      titleVisibility: .visible
    ) {
      Button("Stop Proxy", role: .destructive) {
        model.setProxyEnabled(id: proxyID, enabled: false)
      }
      Button("Cancel", role: .cancel) {}
    } message: {
      Text("Apps using this proxy will not connect until it restarts.")
    }
    .confirmationDialog(
      "Rotate API Key for \(proxy?.name ?? "This Proxy")?",
      isPresented: $confirmRotate,
      titleVisibility: .visible
    ) {
      Button("Rotate Key", role: .destructive) {
        model.rotateProxyKey(id: proxyID)
      }
      Button("Cancel", role: .cancel) {}
    } message: {
      Text("Apps using the current API key will be disconnected until you update them with the new key.")
    }
    .onChange(of: model.copiedKey) { _, copied in
      guard copied?.id == proxyID else { return }
      didCopyApiKey = true
      DispatchQueue.main.asyncAfter(deadline: .now() + 1.4) {
        didCopyApiKey = false
      }
    }
  }

  private var connectedApps: [DesignApp] {
    guard let proxy else { return [] }
    return model.apps.filter { app in
      guard let route = app.routeOptions.first(where: { $0.id == app.selectedRouteID }) else { return false }
      guard route.kind != .native else { return false }
      return app.id == proxy.provider || route.id.localizedCaseInsensitiveContains(proxy.provider)
    }
  }

  private var servingAccountDisplay: String {
    guard let proxy else { return "" }
    let cleanDetail = proxy.detail.trimmingCharacters(in: .whitespacesAndNewlines)
    var accountName = cleanDetail
    if let range = cleanDetail.range(of: " · ") {
      accountName = String(cleanDetail[..<range.lowerBound]).trimmingCharacters(in: .whitespacesAndNewlines)
    }
    if let match = model.accounts.first(where: {
      $0.name.localizedCaseInsensitiveCompare(accountName) == .orderedSame
        || proxy.id.localizedCaseInsensitiveContains($0.id)
    }) {
      let identity = accountIdentityLine(provider: match.provider, detail: match.detail)
      return identity.isEmpty ? match.name : "\(match.name) (\(identity))"
    }
    return accountName.isEmpty ? proxy.detail : accountName
  }

  private func proxyActions(for proxy: DesignProxy) -> some View {
    DesignPaneActions {
      DesignSheetButton(
        title: "Restart Proxy",
        enabled: !model.isBusy
          && canRestartProxy(isRunning: proxy.status == .running, status: proxy.status)
          && model.serviceOperations[proxy.id] == nil,
        identifier: "proxies.inspector.restart"
      ) { model.restartProxy(id: proxy.id) }
    }
  }

  @ViewBuilder
  private func proxyLifecycleAction(_ proxy: DesignProxy) -> some View {
    let busyRow = model.serviceOperations[proxy.id] != nil
    let busy = model.isBusy || busyRow
    Group {
      if proxy.status == .running {
        Button(role: .destructive) {
          confirmStop = true
        } label: {
          if busyRow { ProgressView().controlSize(.small) } else { Text("Stop") }
        }
        .disabled(busy)
        .accessibilityLabel(busyRow ? "Stopping \(proxy.name)" : "Stop \(proxy.name)")
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
    .accessibilityIdentifier("proxies.inspector.lifecycle")
  }

  private var providerName: String {
    guard let proxy else { return "Proxy" }
    return nativeProviderName(proxy.provider)
  }

  private var purpose: String {
    guard let proxy else { return "Local compatibility service" }
    return "\(nativeProviderName(proxy.provider)) compatibility"
  }

  private func copyAddress() {
    guard let proxy else { return }
    let address = "http://127.0.0.1:\(proxy.port)"
    let copied = (NSPasteboard.general.clearContents() != 0)
      && NSPasteboard.general.setString(address, forType: .string)
    guard copied else {
      model.announceOnly("Couldn’t copy \(proxy.name) address.")
      return
    }
    model.announceOnly("Copied \(proxy.name) address.")
    didCopy = true
    DispatchQueue.main.asyncAfter(deadline: .now() + 1.4) {
      didCopy = false
    }
  }

}
