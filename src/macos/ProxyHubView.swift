import AppKit
import SwiftUI

struct ProxyHubView: View {
  @ObservedObject var model: MacOSDesignModel
  @State private var confirmStop = false

  var body: some View {
    DesignGroupedPane {
      Section {
        // Status only — no inline buttons. Start/Restart live once, in the
        // footer (hubActions). A value + two buttons in one row reads
        // cluttered next to the quiet Address/Published rows.
        LabeledContent("Status") {
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
          hubSourceFooter(isLast: true)
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
            hubSourceFooter(isLast: index == hubSourceGroups.count - 1)
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
            hubSourceFooter(isLast: index == hubSourceGroups.count - 1)
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
          VStack(alignment: .leading) {
            Text("One choice applies to every model ID in this group.")
            hubActions
          }
        }
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

  @ViewBuilder
  private var hubActions: some View {
    DesignPaneActions {
      DesignSheetButton(
        title: "Restart Hub",
        enabled: !model.isBusy && model.proxyHubEnabled && model.serviceOperations["hub"] == nil,
        identifier: "hub.restart"
      ) { model.restartProxy(id: "hub") }
      DesignSheetButton(
        title: model.proxyHubEnabled ? "Stop Hub" : "Start Hub",
        enabled: (canStartHub || model.proxyHubEnabled)
          && !model.isBusy
          && model.serviceOperations["hub"] == nil,
        destructive: model.proxyHubEnabled,
        identifier: "hub.enabled"
      ) {
        if model.proxyHubEnabled {
          confirmStop = true
        } else {
          model.setProxyHubEnabled(true)
        }
      }
    }
  }

  /// Footer of a hub-source section. The buttons live under the true last
  /// group: this footer when there is no Model Routing section, that
  /// section's footer otherwise.
  @ViewBuilder
  private func hubSourceFooter(isLast: Bool) -> some View {
    if isLast {
      if model.routingConflicts.isEmpty {
        VStack(alignment: .leading) {
          Text(hubMembershipFooter)
          hubActions
        }
      } else {
        Text(hubMembershipFooter)
      }
    }
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
    let serviceID = hubSourceServiceID(source.id)
    VStack(alignment: .leading, spacing: 4) {
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
      .disabled(model.isBusy || model.serviceOperations[serviceID] != nil)
      if model.serviceOperations[serviceID] != nil {
        HStack(spacing: 6) {
          ProgressView()
            .controlSize(.small)
            .accessibilityLabel("Updating \(source.name)")
          Text(model.serviceOperations[serviceID]?.label ?? "Working…")
            .font(.subheadline)
            .foregroundStyle(.secondary)
        }
      } else if let failure = model.serviceFailures[serviceID] {
        HStack(spacing: 4) {
          DesignWarningSymbol()
          Text(failure)
            .font(.subheadline)
            .foregroundStyle(.secondary)
            .lineLimit(2)
            .fixedSize(horizontal: false, vertical: true)
        }
        .accessibilityIdentifier("hub.account.\(source.id).error")
      }
    }
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
    let serviceID = hubConflictServiceID(conflict.id)
    let resolving = model.serviceOperations[serviceID] != nil
    LabeledContent {
      HStack(spacing: 6) {
        if resolving {
          ProgressView()
            .controlSize(.small)
            .accessibilityLabel("Applying choice for \(conflict.title)")
        }
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
        .disabled(model.isBusy || conflict.candidates.isEmpty || resolving)
      }
    } label: {
      Text(conflict.title)
    }
    .accessibilityIdentifier("hub.conflict.\(conflict.id)")
    if let failure = model.serviceFailures[serviceID] {
      Text(failure)
        .font(.footnote)
        .foregroundStyle(.secondary)
        .fixedSize(horizontal: false, vertical: true)
        .accessibilityIdentifier("hub.conflict.\(conflict.id).error")
    }
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
