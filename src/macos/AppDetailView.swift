import SwiftUI

struct AppDetailView: View {
  @ObservedObject var model: MacOSDesignModel
  let appID: String
  @State private var showingRoutes = false
  @State private var showingModels = false
  @State private var confirmReset = false

  private var app: DesignApp? { model.apps.first { $0.id == appID } }

  private var selectedRoute: DesignRoute? {
    app?.routeOptions.first { $0.id == app?.selectedRouteID }
  }

  private var supportsManagedRouting: Bool {
    app?.routeOptions.contains { $0.kind != .native } == true
  }

  private var showsUsageSection: Bool {
    guard let app, model.showUsage else { return false }
    return !app.usageWindows.isEmpty || isAppRouteConfigured(app)
  }

  @ViewBuilder
  private var appActions: some View {
    // Board JC9e8 + System Settings (Displays › Advanced…): pane-level
    // actions sit under the last group as real bordered controls,
    // never floating plain text.
    if supportsManagedRouting {
      DesignPaneActions {
        DesignSheetButton(
          title: "Reset App Settings",
          enabled: !model.isBusy,
          destructive: true,
          identifier: "app.reset"
        ) { confirmReset = true }
      }
    }
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
              Text(headerSubtitle(app))
                .foregroundStyle(.secondary)
            }
          } icon: {
            DesignProviderIcon(provider: app.id, fallbackSymbol: app.symbol)
          }
        }

        Section {
          LabeledContent("Uses", value: selectedName(app))
          if let detail = selectedRoute?.detail, !detail.isEmpty {
            LabeledContent("Identity", value: detail)
          }
          Button {
            showingRoutes = true
          } label: {
            HStack(spacing: 8) {
              Text("Change Connection")
                .layoutPriority(1)
              Spacer(minLength: 8)
              Text("Accounts, API gateways, and Proxy Hub")
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .truncationMode(.tail)
              Image(systemName: "chevron.right")
                .foregroundStyle(.tertiary)
                .accessibilityHidden(true)
            }
            .contentShape(Rectangle())
          }
          .buttonStyle(.plain)
          .disabled(model.isBusy || app.routeOptions.isEmpty)
          .accessibilityIdentifier("app.connection.change")
        } header: {
          Text("Connection")
        }

        if showsModelConfiguration {
          Section {
            LabeledContent("Default", value: defaultModel(app))
            LabeledContent("Overrides", value: overrideSummary(app))
            Button {
              showingModels = true
            } label: {
              HStack(spacing: 8) {
                Text("Manage Models")
                  .layoutPriority(1)
                Spacer(minLength: 8)
                Text("Default and role overrides")
                  .foregroundStyle(.secondary)
                  .lineLimit(1)
                  .truncationMode(.tail)
                Image(systemName: "chevron.right")
                  .foregroundStyle(.tertiary)
                  .accessibilityHidden(true)
              }
              .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .disabled(!app.modelEditingAvailable || model.isBusy)
            .accessibilityIdentifier("app.models.manage")
          } header: {
            Text("Models")
          } footer: {
            VStack(alignment: .leading) {
              if !app.modelEditingAvailable, let reason = app.modelUnavailableReason,
                !reason.isEmpty
              {
                Text(reason)
              }
              if !showsUsageSection {
                appActions
              }
            }
          }
        }

        if model.showUsage, !app.usageWindows.isEmpty {
          Section {
            ForEach(app.usageWindows) { window in
              usageRow(window)
            }
          } header: {
            Text("Usage")
          } footer: {
            appActions
          }
        } else if model.showUsage, isAppRouteConfigured(app) {
          // A10: a configured login with no quota API says so — as a normal
          // boxed row like every other note in this pane.
          Section {
            Text("This login doesn’t report usage.")
              .foregroundStyle(.secondary)
          } header: {
            Text("Usage")
          } footer: {
            appActions
          }
        }
      }
    }
    .sheet(isPresented: $showingRoutes) {
      AppRouteSheet(model: model, appID: appID)
    }
    .sheet(isPresented: $showingModels) {
      AppModelsSheet(model: model, appID: appID)
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

  /// "Claude account · Ready": the provider kind reads from the selected
  /// route, so a gateway or hub route never poses as an account.
  private func headerSubtitle(_ app: DesignApp) -> String {
    let provider = selectedRoute?.providerID.map(nativeProviderName) ?? nativeProviderName(app.id)
    let kind = selectedRoute?.kind == .gateway ? "gateway" : "account"
    return "\(provider) \(kind) · \(app.status.rawValue)"
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
    let roles = app.modelRoles.filter { $0.id != "default" }
    guard !roles.isEmpty else { return "Same as Default" }
    return roles.map { "\($0.label): \($0.modelID.isEmpty ? "Same as Default" : $0.modelID)" }
      .joined(separator: " · ")
  }

  private func usageRow(_ window: DesignUsageWindow) -> some View {
    VStack(alignment: .leading, spacing: 6) {
      HStack {
        Text(window.label)
        Spacer()
        Text("\(window.remainingPercent)% remaining")
          .foregroundStyle(.secondary)
          .monospacedDigit()
      }
      ProgressView(value: Double(window.remainingPercent), total: 100)
      if window.resetsAtMs != nil || usageWindowPeriod(limitWindowSeconds: window.limitWindowSeconds) != nil {
        HStack(spacing: 4) {
          if let resetsAtMs = window.resetsAtMs {
            Text("Resets")
            Text(Date(timeIntervalSince1970: resetsAtMs / 1000), style: .relative)
          }
          if let period = usageWindowPeriod(limitWindowSeconds: window.limitWindowSeconds) {
            Text(window.resetsAtMs != nil ? "· every \(period)" : "Every \(period)")
          }
        }
        .font(.caption)
        .foregroundStyle(.secondary)
      }
    }
    .accessibilityIdentifier("app.usage.\(window.id)")
  }
}
