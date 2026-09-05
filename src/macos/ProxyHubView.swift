import AppKit
import SwiftUI

struct ProxyHubView: View {
  @ObservedObject var model: MacOSDesignModel
  @State private var confirmStop = false

  var body: some View {
    DesignGroupedPane {
      Section {
        LabeledContent("Status") {
          HStack(spacing: 8) {
            if let operation = model.serviceOperations["hub"] {
              HStack(spacing: 6) {
                ProgressView()
                  .controlSize(.small)
                Text(operation.label)
                  .foregroundStyle(.secondary)
              }
            } else {
              DesignStatusLabel(status: hubStatus)
            }
            Spacer(minLength: 8)
            Button(model.proxyHubEnabled ? "Stop" : "Start") {
              if model.proxyHubEnabled {
                confirmStop = true
              } else {
                model.setProxyHubEnabled(true)
              }
            }
            .buttonStyle(.bordered)
            .controlSize(.small)
            .disabled(!canStartHub && !model.proxyHubEnabled || model.isBusy || model.serviceOperations["hub"] != nil)
            .help(startHelp)
            .accessibilityIdentifier("hub.toggle-inline")
            Button("Restart") { model.restartProxy(id: "hub") }
              .buttonStyle(.bordered)
              .controlSize(.small)
              .disabled(
                model.isBusy || !model.proxyHubEnabled || model.serviceOperations["hub"] != nil
              )
              .accessibilityIdentifier("hub.restart-inline")
          }
        }
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Proxy Hub status \(hubStatus.rawValue). \(startHelp)")
        LabeledContent("Address") {
          HStack(spacing: 8) {
            Text("http://127.0.0.1:\(model.proxyHubPort)")
              .foregroundStyle(.secondary)
              .textSelection(.enabled)
              .lineLimit(1)
              .truncationMode(.middle)
            Button(action: copyAddress) {
              Image(systemName: "doc.on.doc")
            }
            .buttonStyle(.bordered)
            .controlSize(.small)
            .help("Copy Proxy Hub address")
            .accessibilityLabel("Copy Proxy Hub address")
          }
        }
        NavigationLink(value: DesignRouteID.publishedModels) {
          LabeledContent("Published Models") {
            Text(publishedSummary)
              .foregroundStyle(.secondary)
          }
        }
        .accessibilityIdentifier("hub.models")
      } header: {
        Text("Service")
      } footer: {
        Text(serviceFooter)
      }

      if let failure = model.serviceFailures["hub"] {
        Section {
          Label {
            VStack(alignment: .leading, spacing: 2) {
              Text("Proxy Hub Couldn’t Start")
              Text(failure)
                .foregroundStyle(.secondary)
            }
          } icon: {
            DesignWarningSymbol()
          }
        } footer: {
          Text("No app settings were changed. Adjust the conflicting service, then try again.")
        }
      }

      if model.hubSources.isEmpty {
        Section {
          LabeledContent {
            Button("Open Accounts & APIs") { model.destination = .accounts }
          } label: {
            VStack(alignment: .leading, spacing: 2) {
              Text("Add a Compatible Account")
              Text("Add a compatible saved account before starting Proxy Hub.")
                .foregroundStyle(.secondary)
            }
          }
        } header: {
          Text("Accounts in This Hub")
        } footer: {
          Text(hubMembershipFooter)
        }
      } else if usableModelCount == 0 && !model.proxyHubEnabled {
        Section {
          LabeledContent {
            Button("Open Accounts & APIs") { model.destination = .accounts }
              .accessibilityIdentifier("hub.empty-recovery")
          } label: {
            Label {
              VStack(alignment: .leading, spacing: 2) {
                Text("No Models to Publish Yet")
                Text("Sources are on but publish 0 usable models. Add a compatible account or resolve the catalog — starting now would serve nothing.")
                  .foregroundStyle(.secondary)
              }
            } icon: {
              DesignWarningSymbol()
            }
          }
        } header: {
          Text("Accounts in This Hub")
        }
        ForEach(Array(hubSourceGroups.enumerated()), id: \.element.provider) { index, group in
          Section {
            ForEach(group.sources) { source in
              hubSourceRow(source)
            }
          } header: {
            Text(hubSourceGroups.count == 1 ? "Accounts in This Hub" : group.provider)
          } footer: {
            if index == hubSourceGroups.count - 1 {
              Text(hubMembershipFooter)
            }
          }
        }
      } else {
        ForEach(Array(hubSourceGroups.enumerated()), id: \.element.provider) { index, group in
          Section {
            ForEach(group.sources) { source in
              hubSourceRow(source)
            }
          } header: {
            Text(hubSourceGroups.count == 1 ? "Accounts in This Hub" : group.provider)
          } footer: {
            if index == hubSourceGroups.count - 1 {
              Text(hubMembershipFooter)
            }
          }
        }
      }

      if !model.routingConflicts.isEmpty {
        Section {
          ForEach(model.routingConflicts) { conflict in
            conflictPicker(conflict)
          }
        } header: {
          Text("Model Routing")
        } footer: {
          Text("One choice applies to every model ID in this group.")
        }
      }

    } actions: {
      Button("Restart Hub") { model.restartProxy(id: "hub") }
        .disabled(
          model.isBusy || !model.proxyHubEnabled || model.serviceOperations["hub"] != nil
        )
        .accessibilityIdentifier("hub.restart")
      Button(
        model.proxyHubEnabled ? "Stop Hub" : "Start Hub",
        role: model.proxyHubEnabled ? .destructive : nil
      ) {
        if model.proxyHubEnabled {
          confirmStop = true
        } else {
          model.setProxyHubEnabled(true)
        }
      }
      .disabled(!canStartHub && !model.proxyHubEnabled || model.isBusy || model.serviceOperations["hub"] != nil)
      .help(startHelp)
      .accessibilityIdentifier("hub.enabled")
    }
    .toolbar {
      ToolbarItem(placement: .primaryAction) {
        Button(model.proxyHubEnabled ? "Stop Hub" : "Start Hub") {
          if model.proxyHubEnabled {
            confirmStop = true
          } else {
            model.setProxyHubEnabled(true)
          }
        }
        .disabled(!canStartHub && !model.proxyHubEnabled || model.isBusy || model.serviceOperations["hub"] != nil)
        .help(startHelp)
        .accessibilityIdentifier("hub.toolbar.toggle")
      }
    }
    .confirmationDialog(
      "Stop Proxy Hub?",
      isPresented: $confirmStop,
      titleVisibility: .visible
    ) {
      Button("Stop Hub", role: .destructive) { model.setProxyHubEnabled(false) }
      Button("Cancel", role: .cancel) {}
    } message: {
      Text("Apps using the shared Hub address will not connect until it restarts.")
    }
  }

  private var hubMembershipFooter: String {
    "Include a saved account or gateway to publish its models on the Hub address. Independent per-account services appear under Proxies."
  }

  private var hubSourceGroups: [(provider: String, sources: [DesignHubSource])] {
    let groups = Dictionary(grouping: model.hubSources, by: \.provider)
    return groups.keys.sorted().map { provider in
      (
        provider,
        groups[provider]!.sorted {
          $0.name.localizedStandardCompare($1.name) == .orderedAscending
        }
      )
    }
  }

  private var serviceFooter: String {
    if !model.routingConflicts.isEmpty {
      return "Only model IDs with a chosen source are visible to apps."
    }
    if model.hubSourceIDs.isEmpty {
      return "Turn on a model source before starting Proxy Hub."
    }
    if usableModelCount == 0 {
      return "No usable models yet — add a compatible account or resolve the catalog, then start."
    }
    return "All published model IDs are available to apps."
  }

  private var usableModelCount: Int { usablePublishedModelCount(model.publishedModels) }

  private var canStartHub: Bool {
    !model.hubSourceIDs.isEmpty && usableModelCount > 0
  }

  private var startHelp: String {
    if model.proxyHubEnabled { return "Stop Proxy Hub" }
    if model.hubSourceIDs.isEmpty { return "Turn on a model source first" }
    if usableModelCount == 0 { return "No usable models to publish yet" }
    return "Start Proxy Hub"
  }

  private var hubStatus: DesignStatus {
    if model.serviceFailures["hub"] != nil { return .attention }
    return model.proxyHubEnabled ? .running : .stopped
  }

  private var publishedSummary: String {
    let published = model.publishedModels.filter { !$0.needsChoice }.count
    let waiting = model.publishedModels.filter(\.needsChoice).count
    if waiting == 0 { return "\(published) published" }
    return "\(published) published · \(waiting) waiting"
  }

  @ViewBuilder
  private func hubSourceRow(_ source: DesignHubSource) -> some View {
    Toggle(
      isOn: Binding(
        get: { model.hubSourceIDs.contains(source.id) },
        set: { model.setHubSource(source.id, enabled: $0) }
      )
    ) {
      Label {
        VStack(alignment: .leading, spacing: 2) {
          Text(source.name)
          HStack(spacing: 4) {
            if let warning = source.warning, !warning.isEmpty {
              DesignWarningSymbol()
              Text(warning)
                .foregroundStyle(.secondary)
                .lineLimit(1)
            } else {
              Text(hubSourceDetailText(source))
                .foregroundStyle(.secondary)
                .lineLimit(1)
            }
          }
          .font(.subheadline)
        }
      } icon: {
        DesignProviderIcon(provider: source.provider, fallbackSymbol: source.symbol)
      }
    }
    .disabled(model.isBusy)
    .accessibilityIdentifier("hub.account.\(source.id)")
  }

  private func hubSourceDetailText(_ source: DesignHubSource) -> String {
    if source.enabled, source.modelCount > 0 {
      let models = "\(source.modelCount) \(source.modelCount == 1 ? "model" : "models")"
      return source.detail.isEmpty ? models : "\(source.detail) · \(models)"
    }
    return source.detail
  }

  @ViewBuilder
  private func conflictPicker(_ conflict: DesignRoutingConflict) -> some View {
    LabeledContent {
      Picker(
        "Account",
        selection: Binding(
          get: { conflict.selectedCandidateID },
          set: { candidateID in
            guard let candidateID,
                  let candidate = conflict.candidates.first(where: { $0.id == candidateID })
            else { return }
            model.selectRoutingCandidate(actionID: candidate.actionID)
          }
        )
      ) {
        Text("Choose").tag(String?.none)
        ForEach(conflict.candidates) { candidate in
          Text(conflictCandidateLabel(candidate))
            .tag(Optional(candidate.id))
        }
      }
      .labelsHidden()
      .fixedSize()
      .disabled(model.isBusy || conflict.candidates.isEmpty)
    } label: {
      Text(conflict.title)
    }
    .accessibilityIdentifier("hub.conflict.\(conflict.id)")
  }

  private func conflictCandidateLabel(_ candidate: DesignRoutingCandidate) -> String {
    if candidate.detail.isEmpty { return candidate.label }
    return "\(candidate.label) — \(candidate.detail)"
  }

  private func copyAddress() {
    NSPasteboard.general.clearContents()
    NSPasteboard.general.setString("http://127.0.0.1:\(model.proxyHubPort)", forType: .string)
    model.announceOnly("Copied Proxy Hub address.")
  }
}
