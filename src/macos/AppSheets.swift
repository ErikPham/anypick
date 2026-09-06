import SwiftUI

struct AppRouteSheet: View {
  private enum Stage {
    case connection
    case models
    case review
    case applying
    case success
    case recovery
  }

  @ObservedObject var model: MacOSDesignModel
  let appID: String
  @Environment(\.dismiss) private var dismiss
  @State private var routeID = ""
  @State private var stage: Stage = .connection

  private var app: DesignApp? { model.apps.first { $0.id == appID } }

  private var accountRoutes: [DesignRoute] {
    guard let app else { return [] }
    return compatibleAccountRoutes(in: app)
  }

  private var hubRoute: DesignRoute? {
    app?.routeOptions.first { $0.kind == .hub }
  }

  private var gatewayRoutes: [DesignRoute] {
    app?.routeOptions.filter { $0.kind == .gateway } ?? []
  }

  private var selectedRoute: DesignRoute? {
    app?.routeOptions.first { $0.id == routeID }
  }

  private var currentRoute: DesignRoute? {
    app?.routeOptions.first { $0.id == app?.selectedRouteID }
  }

  private var primaryTitle: String {
    switch stage {
    case .connection, .models: "Continue"
    case .review: "Apply Changes"
    case .applying: "Applying…"
    case .success: "Done"
    case .recovery: "Try Again"
    }
  }

  var body: some View {
    DesignSheetHost(
      title: app.map { "Change \($0.name) Connection" } ?? "Choose a Connection",
      width: 480,
      primaryTitle: primaryTitle,
      primaryDisabled: routeID.isEmpty || stage == .applying,
      primaryIdentifier: "app-route.primary.\(stageIdentifier)",
      showsCancel: stage != .applying && stage != .success,
      onCancel: { dismiss() },
      onPrimary: advance
    ) {
      Form {
        switch stage {
        case .connection:
          connectionForm
        case .models:
          modelImpactForm
        case .review:
          reviewForm
        case .applying:
          applyingForm
        case .success:
          resultForm(succeeded: true)
        case .recovery:
          resultForm(succeeded: false)
        }
      }
      .accessibilityIdentifier("app-route.stage.\(stageIdentifier)")
      .onAppear { routeID = app?.selectedRouteID ?? "" }
      .onChange(of: model.switchPhase) { _, phase in
        switch phase {
        case .success(let id, _) where id == appID:
          stage = .success
        case .recovery(let id, _, _, _) where id == appID:
          stage = .recovery
        default:
          break
        }
      }
    }
  }

  @ViewBuilder
  private var connectionForm: some View {
    if !accountRoutes.isEmpty {
            Section {
              Picker("Account", selection: $routeID) {
                ForEach(accountRoutes) { route in
                  Label {
                    Text(route.name)
                  } icon: {
                    DesignProviderIcon(
                      provider: model.providerID(forRoute: route),
                      fallbackSymbol: route.symbol,
                      size: 16
                    )
                  }
                  .tag(route.id)
                }
              }
              .pickerStyle(.radioGroup)
              .labelsHidden()
              .accessibilityIdentifier("app-route.picker")
            } header: {
              Text("Direct & Saved Accounts")
            }
    }

    if let hub = hubRoute {
            Section {
              Picker("Service", selection: $routeID) {
                Text(hub.name).tag(hub.id)
              }
              .pickerStyle(.radioGroup)
              .labelsHidden()
            } header: {
              Text("Shared")
            }
    }

    if !gatewayRoutes.isEmpty {
            Section {
              Picker("Gateway", selection: $routeID) {
                ForEach(gatewayRoutes) { route in
                  Label {
                    Text(route.name)
                  } icon: {
                    DesignProviderIcon(
                      provider: model.providerID(forRoute: route),
                      fallbackSymbol: route.symbol,
                      size: 16
                    )
                  }
                  .tag(route.id)
                }
              }
              .pickerStyle(.radioGroup)
              .labelsHidden()
            } header: {
              Text("API Gateways")
            } footer: {
              Text("Incompatible options are hidden.")
            }
    }
  }

  @ViewBuilder
  private var modelImpactForm: some View {
    Section("Models") {
      if let app, app.modelRoles.isEmpty {
        LabeledContent("Selection", value: "Managed by the app")
      } else if let app {
        ForEach(app.modelRoles) { role in
          LabeledContent(role.label, value: role.modelID.isEmpty ? "Automatic" : role.modelID)
        }
      }
    }
    Section {
      Text("Compatible model choices are preserved. Choices unavailable on the new connection return to Automatic; you can configure them after the catalog refreshes.")
        .foregroundStyle(.secondary)
    }
  }

  @ViewBuilder
  private var reviewForm: some View {
    Section("Connection") {
      LabeledContent("From", value: currentRoute?.name ?? "Not selected")
      LabeledContent("To", value: selectedRoute?.name ?? "Not selected")
    }
    Section("AnyPick Will") {
      Label("Back up the app’s current settings", systemImage: "externaldrive.badge.timemachine")
      Label("Start a required local proxy", systemImage: "server.rack")
      Label("Write the new app settings atomically", systemImage: "checkmark.shield")
    }
  }

  @ViewBuilder
  private var applyingForm: some View {
    Section {
      HStack(spacing: 12) {
        ProgressView()
          .controlSize(.small)
        VStack(alignment: .leading, spacing: 2) {
          Text("Applying changes…")
          Text("Keep AnyPick open while settings and required services are updated.")
            .foregroundStyle(.secondary)
        }
      }
    }
  }

  @ViewBuilder
  private func resultForm(succeeded: Bool) -> some View {
    Section {
      Label {
        VStack(alignment: .leading, spacing: 2) {
          Text(succeeded ? "Connection updated" : "Previous settings restored")
          Text(
            succeeded
              ? "\(app?.name ?? "The app") now uses \(selectedRoute?.name ?? "the selected connection")."
              : "The requested connection could not be applied. The app still uses \(currentRoute?.name ?? "its previous connection")."
          )
          .foregroundStyle(.secondary)
        }
      } icon: {
        Image(systemName: succeeded ? "checkmark.circle.fill" : "exclamationmark.triangle.fill")
          .foregroundStyle(succeeded ? Color.green : Color.orange)
      }
    }
    if !succeeded {
      Section {
        Text("Review the connection and try again. No partial app settings were kept.")
          .foregroundStyle(.secondary)
      }
    }
  }

  private var stageIdentifier: String {
    switch stage {
    case .connection: "connection"
    case .models: "models"
    case .review: "review"
    case .applying: "applying"
    case .success: "success"
    case .recovery: "recovery"
    }
  }

  private func advance() {
    switch stage {
    case .connection:
      stage = .models
    case .models:
      stage = .review
    case .review:
      guard routeID != app?.selectedRouteID else {
        dismiss()
        return
      }
      stage = .applying
      model.setRoute(appID: appID, routeID: routeID)
    case .applying:
      break
    case .success:
      dismiss()
    case .recovery:
      stage = .review
    }
  }
}

struct AppModelsSheet: View {
  @ObservedObject var model: MacOSDesignModel
  let appID: String
  @Environment(\.dismiss) private var dismiss
  @State private var values = [String: String]()
  /// Production save waits for the engine result instead of dismissing on
  /// accept: failures keep field context with the error inline.
  @State private var awaitingSaveResult = false
  @State private var saveError: String?

  private var app: DesignApp? { model.apps.first { $0.id == appID } }

  var body: some View {
    DesignSheetHost(
      title: app.map { "Models for \($0.name)" } ?? "Models",
      width: 480,
      primaryTitle: "Save",
      primaryDisabled: values["default", default: ""].isEmpty || model.isBusy
        || awaitingSaveResult,
      onCancel: { dismiss() },
      onPrimary: save
    ) {
      Form {
        if let app {
          Section {
            ForEach(app.modelRoles) { role in
              Picker(
                role.label,
                selection: Binding(
                  get: { values[role.id] ?? role.modelID },
                  set: { values[role.id] = $0 }
                )
              ) {
                if role.id != "default" { Text("Same as Default").tag("") }
                ForEach(app.modelOptions, id: \.self) { Text($0).tag($0) }
              }
            }
          } header: {
            Text("Models")
          } footer: {
            Text("Changing models doesn’t switch the account.")
          }
        }
        if let saveError {
          Section {
            Text(saveError)
              .foregroundStyle(.secondary)
              .accessibilityIdentifier("app-models.error")
          }
        }
      }
      .onAppear {
        values = Dictionary(uniqueKeysWithValues: app?.modelRoles.map { ($0.id, $0.modelID) } ?? [])
      }
      .onChange(of: model.operationResult) { _, result in
        guard awaitingSaveResult, result?.operation == "app-model-roles" else { return }
        awaitingSaveResult = false
        if result?.succeeded == true {
          dismiss()
        } else if let message = result?.message, !message.isEmpty {
          saveError = message
        } else {
          saveError = "Couldn’t save those models. Try again."
        }
      }
    }
  }

  private func save() {
    guard let app else { return }
    saveError = nil
    // applyModelRoles refuses while another mutation is in flight and the
    // void return cannot say so — the disabled primary covers the steady
    // state, this covers the race.
    guard !model.isBusy else { return }
    if model.isProduction {
      // Dismissal waits for the engine result; the fixture answers
      // synchronously and dismisses at once below.
      if model.updateAppModelRoles(
        appID: appID,
        routeID: app.selectedRouteID,
        modelRoles: values
      ) {
        awaitingSaveResult = true
      } else {
        saveError = "Another change is in progress. Try again in a moment."
      }
      return
    }
    if model.updateAppModelRoles(
      appID: appID,
      routeID: app.selectedRouteID,
      modelRoles: values
    ) {
      dismiss()
    }
  }
}
