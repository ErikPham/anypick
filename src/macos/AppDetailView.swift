import SwiftUI

struct AppDetailView: View {
  @ObservedObject var model: MacOSDesignModel
  let appID: String
  @State private var showingRoutes = false
  @State private var showingModels = false
  @State private var accountSheet: AccountsSheet?
  @State private var awaitingCreatedGateway = false
  @State private var confirmReset = false

  private var app: DesignApp? { model.apps.first { $0.id == appID } }

  private var selectedRoute: DesignRoute? {
    app?.routeOptions.first { $0.id == app?.selectedRouteID }
  }

  private var supportsManagedRouting: Bool {
    app?.routeOptions.contains { $0.kind != .native } == true
  }

  private var showsModelConfiguration: Bool {
    guard let selectedRoute, selectedRoute.kind != .native else { return false }
    return app?.modelRoles.isEmpty == false
  }

  var body: some View {
    DesignGroupedPane {
      if let app {
        Section {
          Label {
            VStack(alignment: .leading, spacing: 2) {
              Text(app.name)
              if !app.detail.isEmpty {
                Text(app.detail)
                  .foregroundStyle(.secondary)
              }
            }
          } icon: {
            DesignProviderIcon(provider: app.id, fallbackSymbol: app.symbol)
          }
        }

        Section {
          LabeledContent("Using", value: selectedName(app))
          if let detail = selectedRoute?.detail, !detail.isEmpty {
            LabeledContent("Details", value: detail)
          }
        } header: {
          Text("Connection")
        }

        if showsModelConfiguration {
          Section {
            LabeledContent("Default", value: defaultModel(app))
            LabeledContent("Overrides", value: overrideSummary(app))
            LabeledContent {
              Button("Configure Models…") { showingModels = true }
                .disabled(!app.modelEditingAvailable || model.isBusy)
            } label: {
              Text("Catalog")
            }
          } header: {
            Text("Models")
          } footer: {
            if !app.modelEditingAvailable, let reason = app.modelUnavailableReason,
              !reason.isEmpty
            {
              Text(reason)
            }
          }
        }

        if model.showUsage, !app.usageWindows.isEmpty {
          Section("Remaining Usage") {
            ForEach(app.usageWindows) { window in
              usageRow(window)
            }
          }
        }
      }

    } actions: {
      if let app {
        Button("Add Account…") { accountSheet = .addAccount }
          .disabled(model.isBusy)
        Button("Add API Gateway…") { accountSheet = .addGateway }
          .disabled(model.isBusy || model.preferredGatewayProvider(for: appID) == nil)
        Button(supportsManagedRouting ? "Change Account or Provider…" : "Switch Account…") {
          showingRoutes = true
        }
        .disabled(model.isBusy || app.routeOptions.isEmpty)
      }
      if supportsManagedRouting {
        Button("Reset App Settings…", role: .destructive) { confirmReset = true }
          .disabled(model.isBusy)
      }
    }
    .sheet(isPresented: $showingRoutes) {
      AppRouteSheet(model: model, appID: appID)
    }
    .sheet(isPresented: $showingModels) {
      AppModelsSheet(model: model, appID: appID)
    }
    .sheet(item: $accountSheet) { sheet in
      switch sheet {
      case .addAccount:
        AddSavedAccountSheet(model: model)
      case .addGateway:
        AddGatewaySheet(model: model, targetClientID: appID) {
          awaitingCreatedGateway = true
          connectCreatedGatewayIfReady(model.pendingGatewayConnectID)
        }
      }
    }
    .onChange(of: model.pendingGatewayConnectID) { _, id in
      connectCreatedGatewayIfReady(id)
    }
    .navigationTitle(app?.name ?? "App")
    .confirmationDialog(
      "Reset settings for this app?",
      isPresented: $confirmReset,
      titleVisibility: .visible
    ) {
      Button("Reset", role: .destructive) { model.resetAppConfiguration(appID: appID) }
      Button("Cancel", role: .cancel) {}
    } message: {
      Text("The official login is kept. Account and model overrides are removed.")
    }
  }

  private func selectedName(_ app: DesignApp) -> String {
    if let selected = app.routeOptions.first(where: { $0.id == app.selectedRouteID })?.name {
      return selected
    }
    if app.detail == "Current login" || app.detail.hasPrefix("Signed in as ") {
      return "Current login"
    }
    return "Not selected"
  }

  private func defaultModel(_ app: DesignApp) -> String {
    app.modelRoles.first(where: { $0.id == "default" })?.modelID ?? app.reasoningModel
  }

  private func overrideSummary(_ app: DesignApp) -> String {
    let overrides = app.modelRoles.filter { $0.id != "default" && !$0.modelID.isEmpty }
    guard !overrides.isEmpty else { return "Same as Default" }
    return overrides.map { "\($0.label): \($0.modelID)" }.joined(separator: " · ")
  }

  private func connectCreatedGatewayIfReady(_ gatewayID: String?) {
    guard awaitingCreatedGateway, let gatewayID else { return }
    awaitingCreatedGateway = false
    model.pendingGatewayConnectID = nil
    model.connectGateway(id: gatewayID, appIDs: [appID])
  }

  private func usageRow(_ window: DesignUsageWindow) -> some View {
    VStack(alignment: .leading, spacing: 6) {
      HStack {
        Text(window.label)
        Spacer()
        Text("\(window.remainingPercent)% left")
          .foregroundStyle(.secondary)
          .monospacedDigit()
      }
      ProgressView(value: Double(window.remainingPercent), total: 100)
      if let resetsAtMs = window.resetsAtMs {
        HStack(spacing: 4) {
          Text("Resets")
          Text(Date(timeIntervalSince1970: resetsAtMs / 1000), style: .relative)
        }
        .font(.caption)
        .foregroundStyle(.secondary)
      }
    }
    .accessibilityIdentifier("app.usage.\(window.id)")
  }
}
