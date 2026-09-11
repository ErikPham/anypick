import SwiftUI

/// Model options are published per connection, so the connection sheet can
/// only offer a picker for the route that is live right now — never for one
/// the user has merely selected. Without options, or without roles to fill,
/// a picker would be a dead control.
func appRouteOffersModelStep(_ app: DesignApp) -> Bool {
  app.modelEditingAvailable && !app.modelOptions.isEmpty && !app.modelRoles.isEmpty
}

/// Whether the sheet should nudge the user into the picker: an app that
/// already has a default model pinned must not be forced through it to
/// finish.
func appRouteNeedsModelChoice(_ app: DesignApp) -> Bool {
  guard appRouteOffersModelStep(app) else { return false }
  return (app.modelRoles.first { $0.id == "default" }?.modelID ?? "").isEmpty
}

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
  @State private var modelValues: [String: String] = [:]
  @State private var modelSaveError: String?
  @State private var awaitingModelSave = false

  private var app: DesignApp? { model.apps.first { $0.id == appID } }

  private var showsModelStep: Bool { app.map(appRouteOffersModelStep) ?? false }

  private var needsModelChoice: Bool {
    guard let app else { return false }
    return appRouteNeedsModelChoice(app)
  }

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
    case .connection: "Continue"
    case .models: "Save Models"
    case .review: "Apply Changes"
    case .applying: "Applying…"
    case .success: needsModelChoice ? "Choose Models" : "Done"
    case .recovery: "Try Again"
    }
  }

  var body: some View {
    DesignSheetHost(
      title: app.map { "Change \($0.name) Connection" } ?? "Choose a Connection",
      width: 480,
      primaryTitle: primaryTitle,
      primaryDisabled: routeID.isEmpty || stage == .applying || awaitingModelSave,
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
          modelForm
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
      .onChange(of: model.operationResult) { _, result in
        guard awaitingModelSave, result?.operation == "app-model-roles" else { return }
        awaitingModelSave = false
        if result?.succeeded == true {
          dismiss()
        } else if let message = result?.message, !message.isEmpty {
          modelSaveError = message
        } else {
          modelSaveError = "Couldn’t save those models. Try again."
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

  /// The editable model step. It runs after the connection is live because
  /// the catalog is per-connection: options published for the previous route
  /// would let the user pick a model the new one cannot serve.
  @ViewBuilder
  private var modelForm: some View {
    if let app {
      Section {
        ForEach(app.modelRoles) { role in
          Picker(
            role.label,
            selection: Binding(
              get: { modelValues[role.id] ?? role.modelID },
              set: { modelValues[role.id] = $0 }
            )
          ) {
            if role.id != "default" { Text("Same as Default").tag("") }
            ForEach(app.modelOptions, id: \.self) { Text($0).tag($0) }
          }
        }
      } header: {
        Text("Models")
      } footer: {
        Text("Published by \(selectedRoute?.name ?? "the current connection"). Changing models doesn’t switch the account.")
      }
    }
    if let modelSaveError {
      Section {
        Text(modelSaveError)
          .foregroundStyle(.secondary)
          .accessibilityIdentifier("app-route.models.error")
      }
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
    Section {
      Text("Compatible model choices are preserved. You choose the models for the new connection on the next step.")
        .foregroundStyle(.secondary)
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
      stage = .review
    case .review:
      guard routeID != app?.selectedRouteID else {
        // Nothing to switch: this is a "change the model on the current
        // connection" run, so go straight to the picker when it exists.
        if showsModelStep {
          seedModelValues()
          stage = .models
        } else {
          dismiss()
        }
        return
      }
      stage = .applying
      model.setRoute(appID: appID, routeID: routeID)
    case .success:
      if showsModelStep {
        seedModelValues()
        stage = .models
      } else {
        dismiss()
      }
    case .models:
      saveModels()
    case .applying:
      break
    case .recovery:
      stage = .review
    }
  }

  private func seedModelValues() {
    modelSaveError = nil
    modelValues = Dictionary(
      uniqueKeysWithValues: app?.modelRoles.map { ($0.id, $0.modelID) } ?? []
    )
  }

  private func saveModels() {
    guard let app else { return }
    guard !model.isBusy else {
      modelSaveError = "Another change is in progress. Try again in a moment."
      return
    }
    modelSaveError = nil
    if model.updateAppModelRoles(
      appID: appID,
      routeID: app.selectedRouteID,
      modelRoles: modelValues
    ) {
      if model.isProduction {
        awaitingModelSave = true
      } else {
        dismiss()
      }
    } else {
      modelSaveError = "Another change is in progress. Try again in a moment."
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
