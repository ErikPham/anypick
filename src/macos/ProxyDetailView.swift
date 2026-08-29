import AppKit
import SwiftUI

struct ProxyDetailView: View {
  @ObservedObject var model: MacOSDesignModel
  let proxyID: String
  @State private var didCopy = false
  @State private var confirmStop = false

  private var proxy: DesignProxy? { model.proxies.first { $0.id == proxyID } }

  var body: some View {
    Group {
      if let proxy {
        DesignGroupedPane {
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
            if let operation = model.serviceOperations[proxy.id] {
              HStack(spacing: 6) {
                ProgressView()
                  .controlSize(.small)
                Text(operation.label)
                  .foregroundStyle(.secondary)
              }
            } else {
              Text(proxy.status.rawValue)
                .foregroundStyle(.secondary)
            }
          }
          LabeledContent("Provider", value: providerName)
        } header: {
          Text("Service")
        }

        if let failure = model.serviceFailures[proxy.id] {
          Section {
            Label {
              VStack(alignment: .leading, spacing: 2) {
                Text("Needs Attention")
                Text(failure)
                  .foregroundStyle(.secondary)
              }
            } icon: {
              DesignWarningSymbol()
            }
          }
        }

        Section {
          LabeledContent("Address") {
            HStack {
              Text("http://127.0.0.1:\(proxy.port)")
                .foregroundStyle(.secondary)
                .textSelection(.enabled)
              Button(didCopy ? "Copied" : "Copy", action: copyAddress)
                .disabled(didCopy)
                .accessibilityIdentifier("proxies.inspector.copy")
            }
          }
          LabeledContent("Logs", value: "Available")
        } header: {
          Text("Endpoint")
        } footer: {
          Text("This proxy listens only on this Mac. Its token isn’t shown.")
        }

        } actions: {
          Button("Restart Proxy") { model.restartProxy(id: proxy.id) }
            .disabled(model.isBusy || !proxy.enabled || model.serviceOperations[proxy.id] != nil)
            .accessibilityIdentifier("proxies.inspector.restart")
          Button(
            proxy.enabled ? "Stop Proxy" : "Start Proxy",
            role: proxy.enabled ? .destructive : nil
          ) {
            if proxy.enabled {
              confirmStop = true
            } else {
              model.setProxyEnabled(id: proxy.id, enabled: true)
            }
          }
          .disabled(model.isBusy || model.serviceOperations[proxy.id] != nil)
          .accessibilityIdentifier("proxies.inspector.toggle")
        }
      } else {
        DesignEmptyState(
          title: "Proxy Is No Longer Available",
          message: "Return to Proxies and choose another service.",
          symbol: "server.rack"
        )
      }
    }
    .navigationTitle(shortName)
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
  }

  private var shortName: String {
    switch proxyID {
    case "claude-relay": "Claude"
    case "codex-relay": "Codex"
    case "kiro-bridge": "Kiro"
    default: proxy?.name ?? "Proxy"
    }
  }

  private var providerName: String {
    switch proxyID {
    case "claude-relay": "Claude"
    case "codex-relay": "Codex"
    case "kiro-bridge": "Kiro"
    default: proxy?.name ?? "Proxy"
    }
  }

  private var purpose: String {
    switch proxyID {
    case "claude-relay": "Claude Code compatibility"
    case "codex-relay": "Codex CLI compatibility"
    case "kiro-bridge": "Kiro compatibility"
    default: proxy?.detail ?? "Local compatibility service"
    }
  }

  private func copyAddress() {
    guard let proxy else { return }
    NSPasteboard.general.clearContents()
    NSPasteboard.general.setString("http://127.0.0.1:\(proxy.port)", forType: .string)
    model.announceOnly("Copied \(proxy.name) address.")
    didCopy = true
    DispatchQueue.main.asyncAfter(deadline: .now() + 1.4) {
      didCopy = false
    }
  }
}
