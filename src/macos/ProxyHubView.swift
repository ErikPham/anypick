import AppKit
import SwiftUI

struct ProxyHubView: View {
  @ObservedObject var model: MacOSDesignModel
  @State private var copiedAddress = false
  @State private var copiedApiKey = false
  @State private var confirmation: DesignConfirmation?

  var body: some View {
    DesignGroupedPane {
      // One control per row, the shape the pane recipe asks for: a labelled
      // Toggle, then the facts as LabeledContent. The previous row packed the
      // switch and an Options button into a single line next to the title,
      // which is what made it read as cramped — and the facts it hid behind a
      // sheet (status, address, published models) are the pane's own job.
      Section {
        // The same row shape as the per-account rows under Proxies: identity,
        // the service's state as the subtitle, and one trailing lifecycle
        // button. A switch said "policy"; this service is started and stopped,
        // and the README puts service lifecycle in the Service row as a button.
        DesignIdentityRow(
          title: "Proxy Hub",
          detail: hubStatusSubtitle
        ) {
          Image(systemName: DesignDestination.proxyHub.symbol)
            .foregroundStyle(.secondary)
        } trailing: {
          hubLifecycleButton
        }
        .contextMenu {
          if model.proxyHubEnabled {
            Button("Restart Hub") { model.restartProxy(id: "hub") }
              .disabled(model.isBusy || model.serviceOperations["hub"] != nil)
          }
        }

        LabeledContent("Address") {
          HStack(spacing: 6) {
            Text(hubAddress)
              .font(.system(.body, design: .monospaced))
              .foregroundStyle(.secondary)
              .textSelection(.enabled)
              .lineLimit(1)
              .truncationMode(.middle)
            DesignCopyButton(
              copied: copiedAddress,
              label: "Copy Proxy Hub address",
              identifier: "hub.copy-address",
              action: copyAddress
            )
          }
        }

        if model.proxyHubCanCopyAPIKey {
          LabeledContent("API Key") {
            HStack(spacing: 6) {
              if model.proxyHubApiKey.isEmpty {
                Text("Hidden until copied")
                  .foregroundStyle(.secondary)
              } else {
                Text(model.proxyHubApiKey)
                  .font(.system(.body, design: .monospaced))
                  .foregroundStyle(.secondary)
                  .textSelection(.enabled)
                  .lineLimit(1)
                  .truncationMode(.middle)
              }
              DesignCopyButton(
                copied: copiedApiKey,
                label: "Copy Proxy Hub API key",
                identifier: "hub.copy-api-key",
                action: { model.copyProxyAPIKey(id: "hub") }
              )
              Button {
                requestRotate()
              } label: {
                Image(systemName: "arrow.triangle.2.circlepath")
              }
              .buttonStyle(.borderless)
              .help("Rotate Proxy Hub API Key")
              .accessibilityLabel("Rotate Proxy Hub API Key")
              .accessibilityIdentifier("hub.rotate-api-key")
            }
          }
        }

        NavigationLink(value: DesignRouteID.publishedModels) {
          LabeledContent("Published Models") {
            HStack(spacing: 6) {
              Text(publishedSummary)
                .foregroundStyle(.secondary)
              if waitingPublishedCount > 0 {
                Text("\(waitingPublishedCount) need choice")
                  .font(.caption)
                  .foregroundStyle(.secondary)
              }
            }
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

      if !actionableRoutingConflicts.isEmpty {
        Section {
          ForEach(actionableRoutingConflicts) { conflict in
            conflictPicker(conflict)
          }
        } header: {
          Text("Routing Conflicts")
        } footer: {
          Text("Multiple providers share these model IDs. Selecting a provider routes client requests to that provider’s accounts.")
        }
      }

      if !switchableModels.isEmpty {
        Section {
          ForEach(switchableModels.prefix(4)) { item in
            let parsed = parseModelIdentity(item.modelID, fallbackProvider: item.ownerProvider)
            let candidates = model.routeCandidates(for: item.modelID)
            let selectedID = activeCandidateID(for: item, candidates: candidates)
            let providerNames = candidates.map { hubProviderDisplayName($0.provider) }
            LabeledContent {
              DesignCandidatePicker(
                candidates: candidates,
                selection: Binding<String?>(
                  get: { selectedID },
                  set: { newID in
                    guard let newID, let cand = candidates.first(where: { $0.id == newID }) else { return }
                    model.selectRoutingCandidate(actionID: cand.actionID)
                  }
                ),
                accessibilityIdentifier: "hub.model-route.\(item.modelID)"
              )
              .fixedSize()
            } label: {
              HStack(spacing: 8) {
                DesignProviderIcon(
                  provider: parsed.vendorKey,
                  fallbackSymbol: parsed.fallbackSymbol,
                  size: 20
                )
                VStack(alignment: .leading, spacing: 2) {
                  Text(item.modelID)
                    .font(.system(.body, design: .monospaced))
                    .lineLimit(1)
                  Text("Available via \(providerNames.joined(separator: ", "))")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                }
              }
            }
          }
          if switchableModels.count > 4 {
            NavigationLink(value: DesignRouteID.publishedModels) {
              Text("View all \(switchableModels.count) routed models…")
                .foregroundStyle(.secondary)
            }
          }
        } header: {
          Text("Model Routing")
        } footer: {
          Text("Choose which provider handles client requests when multiple providers offer the same model.")
        }
      }

      Section {
        if !model.hubSources.contains(where: { !$0.rotates }) {
          LabeledContent {
            Button("Open Accounts & APIs") { model.destination = .accounts }
          } label: {
            VStack(alignment: .leading, spacing: 2) {
              Text("Add a Compatible Account")
              Text("Add a compatible saved account before starting Proxy Hub.")
                .foregroundStyle(.secondary)
            }
          }
        } else {
          ForEach(visibleHubSources) { source in
            hubSourceRow(source)
          }
        }
      } header: {
        Text(accountsSectionTitle)
      } footer: {
        hubSourceFooter(isLast: true)
      }

      if !rotationSources.isEmpty {
        Section {
          ForEach(rotationSources) { rotation in
            strategyPicker(rotation)
          }
        } header: {
          Text("Request Strategy")
        } footer: {
          Text("Failover sends each request to one account. Round Robin spreads each model across the accounts that serve it.")
        }
      }
    }
    .designConfirmation($confirmation)
    .onChange(of: model.copiedKey) { _, copied in
      guard copied?.id == "hub" else { return }
      copiedApiKey = true
      DispatchQueue.main.asyncAfter(deadline: .now() + 1.4) {
        copiedApiKey = false
      }
    }
  }

  private func requestRotate() {
    confirmation = DesignConfirmation(
      id: "rotate-hub-key",
      title: "Rotate Proxy Hub API Key?",
      message:
        "Apps connected to Proxy Hub will be disconnected until you update them with the new key.",
      action: .confirm("Rotate Key") { model.rotateProxyKey(id: "hub") }
    )
  }

  private var hubAddress: String { "http://127.0.0.1:\(model.proxyHubPort)" }

  /// Start / Stop, mirroring `ProxiesView.lifecycleAction`: while the service is
  /// working the button stays and spins inside it, and Stop is destructive
  /// because every app bound through the Hub loses its route until it restarts.
  @ViewBuilder
  private var hubLifecycleButton: some View {
    let busy = model.isBusy || model.serviceOperations["hub"] != nil
    let working = model.serviceOperations["hub"] != nil
    Group {
      if model.proxyHubEnabled {
        Button(role: .destructive) {
          model.setProxyHubEnabled(false)
        } label: {
          ZStack {
            if working { ProgressView().controlSize(.small) } else { Text("Stop") }
          }
          .frame(minWidth: 46)
        }
        .disabled(busy)
        .accessibilityLabel(working ? "Stopping Proxy Hub" : "Stop Proxy Hub")
      } else {
        Button {
          model.setProxyHubEnabled(true)
        } label: {
          ZStack {
            if working { ProgressView().controlSize(.small) } else { Text("Start") }
          }
          .frame(minWidth: 46)
        }
        .disabled(busy)
        .accessibilityLabel(working ? "Starting Proxy Hub" : "Start Proxy Hub")
      }
    }
    .buttonStyle(.bordered)
    .controlSize(.small)
    .accessibilityIdentifier("hub.service-actions")
  }

  private var hubMembershipFooter: String {
    "Include a saved account or gateway to publish its models on the Hub address. Independent per-account services appear under Proxies."
  }

  @ViewBuilder
  private func hubSourceFooter(isLast: Bool) -> some View {
    if isLast {
      Text(hubMembershipFooter)
    }
  }

  private var serviceFooter: String {
    if let op = model.serviceOperations["hub"] {
      switch op {
      case .starting: return "Starting Proxy Hub…"
      case .stopping: return "Stopping Proxy Hub…"
      case .restarting: return "Restarting Proxy Hub…"
      }
    }
    if !model.proxyHubEnabled {
      return "Turn on Proxy Hub to publish models on a single shared address."
    }
    if !model.routingConflicts.isEmpty {
      return "Only models with a resolved provider route are published to apps."
    }
    if usableModelCount == 0 {
      if model.hubSourceIDs.isEmpty {
        return "Turn on a model source before starting Proxy Hub."
      }
      return "No usable models yet — add a compatible account or resolve the catalog, then start."
    }
    return "All published model IDs are available to apps."
  }

  private var usableModelCount: Int { usablePublishedModelCount(model.publishedModels) }

  private var canStartHub: Bool {
    // A published catalog is the source of truth for whether Start can do
    // useful work. Older helpers could leave hubSourceIDs empty while the
    // published-model snapshot was already warm; blocking Start in that state
    // made the menu look broken even though the endpoint was ready to serve.
    usableModelCount > 0
  }

  private var startHelp: String {
    if model.proxyHubEnabled { return "Stop Proxy Hub" }
    if usableModelCount == 0 { return "No usable models to publish yet" }
    return "Start Proxy Hub"
  }

  private var hubStatus: DesignStatus {
    if model.serviceFailures["hub"] != nil { return .attention }
    return model.proxyHubEnabled ? .running : .stopped
  }

  private func copyAddress() {
    let copied = (NSPasteboard.general.clearContents() != 0)
      && NSPasteboard.general.setString(hubAddress, forType: .string)
    guard copied else {
      model.announceOnly("Couldn’t copy Proxy Hub address.")
      return
    }
    copiedAddress = true
    model.announceOnly("Copied Proxy Hub address.")
    DispatchQueue.main.asyncAfter(deadline: .now() + 1.4) {
      copiedAddress = false
    }
  }

  private var hubStatusSubtitle: String {
    if let op = model.serviceOperations["hub"]?.label {
      return op
    }
    if model.proxyHubEnabled {
      return "Running · Port \(model.proxyHubPort)"
    } else {
      return "Stopped"
    }
  }

  private var actionableRoutingConflicts: [DesignRoutingConflict] {
    model.routingConflicts.filter { conflict in
      let providers = Set(conflict.candidates.filter(\.providerChoice).map { $0.provider.lowercased() })
      return providers.count > 1
    }
  }

  private var switchableModels: [DesignPublishedModel] {
    model.publishedModels.filter { item in
      !item.needsChoice && model.routeCandidates(for: item.modelID).count > 1
    }
  }

  private var waitingPublishedCount: Int {
    let conflictModelIDs = Set(actionableRoutingConflicts.flatMap(\.models))
    return model.publishedModels.filter { $0.needsChoice && conflictModelIDs.contains($0.modelID) }.count
  }

  private var publishedSummary: String {
    let published = model.publishedModels.count - waitingPublishedCount
    return "\(published) published"
  }

  private var accountsSectionTitle: String {
    let enabledCount = visibleHubSources.filter { model.hubSourceShowsEnabled($0.id) }.count
    let totalCount = visibleHubSources.count
    if totalCount == 0 { return "Accounts in This Hub" }
    return "Accounts in This Hub (\(enabledCount) of \(totalCount) enabled)"
  }

  /// The rotation row governs the whole hub, so it sorts first; the sources
  /// below it keep their alphabetical order. Selection lives in
  /// DesignHubSourceRows so it is unit-tested, not just rendered.
  private var rotationSources: [DesignHubSource] {
    DesignHubSourceRows.rotationRows(in: model.hubSources)
  }

  private var visibleHubSources: [DesignHubSource] {
    DesignHubSourceRows.visible(
      model.hubSources.map { source in
        var row = source
        row.name = cleanSourceDisplayName(row.name)
        return row
      }
    )
  }

  @ViewBuilder
  private func strategyPicker(_ rotation: DesignHubSource) -> some View {
    let serviceID = hubSourceServiceID(rotation.id)
    VStack(alignment: .leading, spacing: 4) {
      Picker("Strategy", selection: Binding(
        get: { model.hubSourceShowsEnabled(rotation.id) },
        set: { enabled in model.setHubRotation(rotation.hub.isEmpty ? "default" : rotation.hub, enabled: enabled) }
      )) {
        Text("Failover").tag(false)
        Text("Round Robin").tag(true)
      }
      .pickerStyle(.segmented)
      .labelsHidden()
      .disabled(model.isBusy || model.serviceOperations[serviceID] != nil)
      .accessibilityIdentifier(strategyIdentifier(for: rotation))
      if let failure = model.serviceFailures[serviceID] {
        HStack(spacing: 4) {
          DesignWarningSymbol()
          Text(failure)
            .font(.footnote)
            .foregroundStyle(.secondary)
            .lineLimit(1)
            .accessibilityIdentifier("\(strategyIdentifier(for: rotation)).error")
        }
      }
    }
  }

  private func strategyIdentifier(for rotation: DesignHubSource) -> String {
    rotationSources.count > 1 ? "hub.strategy.\(rotation.hub)" : "hub.strategy"
  }

  @ViewBuilder
  private func hubSourceRow(_ source: DesignHubSource) -> some View {
    let serviceID = hubSourceServiceID(source.id)
    let isUpdating = model.serviceOperations[serviceID] != nil
    let failure = model.serviceFailures[serviceID]
    // A rotation row carries its own title and explanation; an account row
    // resolves a display identity from its source id.
    let (rowTitle, rowSubtitle) = source.describesItself
      ? (source.name, source.detail)
      : resolveSourceIdentity(source)
    let rowKey = source.rotates ? "hub.rotation.\(source.id)" : "hub.account.\(source.id)"

    Toggle(
      isOn: Binding(
        get: { model.hubSourceShowsEnabled(source.id) },
        set: { enabled in
          if source.rotates {
            model.setHubRotation(source.hub, enabled: enabled)
          } else {
            model.setHubSource(source.id, enabled: enabled)
          }
        }
      )
    ) {
      Label {
        VStack(alignment: .leading, spacing: 2) {
          Text(rowTitle)
          HStack(spacing: 4) {
            if let failure {
              DesignWarningSymbol()
              Text(failure)
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .accessibilityIdentifier("\(rowKey).error")
            } else if let warning = source.warning, !warning.isEmpty {
              DesignWarningSymbol()
              Text(warning)
                .foregroundStyle(.secondary)
                .lineLimit(1)
              if !rowSubtitle.isEmpty {
                Text("·")
                  .foregroundStyle(.tertiary)
                Text(rowSubtitle)
                  .foregroundStyle(.secondary)
                  .lineLimit(1)
              }
            } else if !rowSubtitle.isEmpty {
              Text(rowSubtitle)
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
    .disabled(model.isBusy || isUpdating)
    .accessibilityIdentifier(rowKey)
  }

  private func resolveSourceIdentity(_ source: DesignHubSource) -> (title: String, subtitle: String) {
    let components = source.id.split(separator: "/").map(String.init)
    let sourceProviderID = components.count >= 4 ? components[2] : ""
    let sourceAccountName = components.count >= 4 ? components[3] : cleanSourceDisplayName(source.name)

    let matchingAccount = model.accounts.first(where: { acc in
      if acc.id == source.id || source.id.hasSuffix("/" + acc.id) || acc.id.hasSuffix("/" + source.id) {
        return true
      }
      if !sourceProviderID.isEmpty && !sourceAccountName.isEmpty {
        let providerMatches = acc.providerID.caseInsensitiveCompare(sourceProviderID) == .orderedSame
          || acc.provider.caseInsensitiveCompare(sourceProviderID) == .orderedSame
        let nameMatches = acc.name.caseInsensitiveCompare(sourceAccountName) == .orderedSame
          || cleanSourceDisplayName(acc.id).caseInsensitiveCompare(sourceAccountName) == .orderedSame
        if providerMatches && nameMatches {
          return true
        }
      }
      let cleanAccName = cleanSourceDisplayName(acc.name)
      let cleanSourceName = cleanSourceDisplayName(source.name)
      let nameMatches = cleanAccName == cleanSourceName || acc.name == source.name
      let providerMatches = acc.provider.caseInsensitiveCompare(source.provider) == .orderedSame
        || acc.providerID.caseInsensitiveCompare(source.provider) == .orderedSame
        || nativeProviderName(acc.providerID).caseInsensitiveCompare(source.provider) == .orderedSame
      return nameMatches && providerMatches
    })

    var identity = source.detail.trimmingCharacters(in: .whitespacesAndNewlines)
    if identity.isEmpty || identity.contains("/") || identity.caseInsensitiveCompare(source.provider) == .orderedSame || identity.caseInsensitiveCompare(source.name) == .orderedSame {
      if let accDetail = matchingAccount?.detail.trimmingCharacters(in: .whitespacesAndNewlines), !accDetail.isEmpty {
        identity = accDetail
      }
    }
    identity = cleanSourceDisplayName(identity)
    if identity.caseInsensitiveCompare(source.provider) == .orderedSame {
      identity = ""
    }

    var cleanName = cleanSourceDisplayName(source.name)
    if cleanName.caseInsensitiveCompare(source.provider) == .orderedSame {
      cleanName = matchingAccount.map { disambiguatedAccountName(for: $0, in: model.accounts) } ?? ""
    }

    let providerDisplay = hubProviderDisplayName(source.provider)
    let title: String
    if cleanName.isEmpty || cleanName.caseInsensitiveCompare(providerDisplay) == .orderedSame {
      title = providerDisplay
    } else {
      title = "\(providerDisplay) — \(cleanName)"
    }

    let isEnabled = model.hubSourceShowsEnabled(source.id)
    let modelsPart: String
    if isEnabled {
      let count = max(0, source.modelCount)
      modelsPart = "\(count) \(count == 1 ? "model" : "models")"
    } else {
      modelsPart = "Excluded from Hub"
    }

    var parts: [String] = []
    if !identity.isEmpty && identity.caseInsensitiveCompare(title) != .orderedSame && identity.caseInsensitiveCompare(cleanName) != .orderedSame {
      parts.append(identity)
    }
    if !modelsPart.isEmpty {
      parts.append(modelsPart)
    }
    let subtitle = parts.joined(separator: " · ")

    return (title, subtitle)
  }

  @ViewBuilder
  private func conflictPicker(_ conflict: DesignRoutingConflict) -> some View {
    let serviceID = hubConflictServiceID(conflict.id)
    let resolving = model.serviceOperations[serviceID] != nil
    let (infoTitle, infoSubtitle) = hubConflictDisplayInfo(conflict)
    let candidatesToShow = uniqueProviderCandidates(conflict.candidates)
    let conflictModelID = conflict.models.first ?? conflict.title
    let parsed = parseModelIdentity(conflictModelID, fallbackProvider: conflict.candidates.first?.provider ?? "")

    LabeledContent {
      DesignCandidatePicker(
        candidates: candidatesToShow,
        selection: Binding<String?>(
          get: {
            if let selected = conflict.selectedCandidateID,
               candidatesToShow.contains(where: { $0.id == selected }) {
              return selected
            }
            return nil
          },
          set: { candidateID in
            guard let candidateID,
                  let candidate = candidatesToShow.first(where: { $0.id == candidateID })
            else { return }
            model.selectRoutingCandidate(actionID: candidate.actionID)
          }
        ),
        placeholder: "Select Provider…",
        accessibilityIdentifier: "hub.conflict.\(conflict.id).picker"
      )
      .fixedSize()
      .disabled(model.isBusy || candidatesToShow.isEmpty || resolving)
    } label: {
      HStack(spacing: 8) {
        DesignProviderIcon(
          provider: parsed.vendorKey,
          fallbackSymbol: parsed.fallbackSymbol,
          size: 20
        )
        VStack(alignment: .leading, spacing: 2) {
          Text(infoTitle)
            .font(.system(.body, design: .monospaced))
          if let failure = model.serviceFailures[serviceID] {
          HStack(spacing: 4) {
            DesignWarningSymbol()
            Text(failure)
              .font(.footnote)
              .foregroundStyle(.secondary)
              .lineLimit(1)
              .accessibilityIdentifier("hub.conflict.\(conflict.id).error")
          }
        } else if resolving {
          Text("Applying choice…")
            .font(.footnote)
            .foregroundStyle(.secondary)
            .accessibilityLabel("Applying choice for \(conflict.title)")
        } else if !infoSubtitle.isEmpty {
          Text(infoSubtitle)
            .font(.footnote)
            .foregroundStyle(.secondary)
            .lineLimit(1)
        }
      }
    }
  }
  .accessibilityIdentifier("hub.conflict.\(conflict.id)")
}
}
