import AppKit
import Combine
import Foundation
import UniformTypeIdentifiers

let anyPickBackupType = UTType(filenameExtension: "anypick-backup") ?? .data

// Adapts the versioned tray snapshot/command protocol to the new design-system UI.
// Secrets remain behind the Node supervisor; this model only sees redacted snapshots.
@MainActor
final class ProductionDesignModel: MacOSDesignModel {
  let store: TrayStore
  private var storeObserver: AnyCancellable?
  private var lastResultId: String?
  private var pendingOperations = [String: String]()
  private var pendingRouteChanges = [(appID: String, routeID: String)]()
  /// Model edits that arrived together with a route change. A route switch
  /// takes the single-flight slot, so the edit waits here and is applied
  /// once the snapshot confirms the new route — the switch never drops it.
  /// Dropped when the switch fails.
  private var pendingModelRoleEdits = [String: (routeID: String, roles: [String: String])]()
  private var pendingConfigEdits = [
    String: (routeID: String, fast: String, reasoning: String, fallback: Bool)
  ]()
  private var serviceRequests = [String: (id: String, operation: DesignServiceOperation)]()
  /// Desired hub-source state per in-flight toggle, keyed by
  /// hubSourceServiceID. Snapshot confirmation (not the faster
  /// command-accepted result) clears the row spinner.
  private var hubSourcePending = [String: Bool]()
  /// Gateway names awaiting creation, keyed by requestID. The post-create
  /// auto-connect arms only on the success result (A9), never on accept.
  private var pendingGatewayNames = [String: String]()
  /// Backup rows awaiting a confirmed forget, keyed by requestID. The staged
  /// server copy and the row clear only on the success result (B3).
  private var pendingForgetBackupIDs = [String: String]()
  /// Settings with an in-flight mutate, keyed by property name. synchronize()
  /// skips overwriting these (S3): the binding already flipped local-first,
  /// and rewriting the stale snapshot value until the supervisor acks reads
  /// as flicker. Cleared by the command result, or when the command loop
  /// goes idle as a backstop.
  private var pendingSettings = Set<String>()

  /// Send a setting mutate, holding the local value until it settles.
  private func sendSetting(key: String, changed: Bool, mutate: () -> String?) {
    guard changed else { return }
    pendingSettings.insert(key)
    guard let requestID = mutate() else {
      pendingSettings.remove(key)
      return
    }
    pendingOperations[requestID] = "setting-\(key)"
    operationResult = nil
  }
  /// Watchdogs that fail a service operation when no snapshot confirms it
  /// in time. Without them a lost snapshot would spin the row forever.
  private var serviceTimeoutTasks = [String: Task<Void, Never>]()
  private var clearedActivityIDs = Set<String>()
  /// Log lines hidden by Clear, per source: the exact tail on screen when
  /// cleared. New lines append visibly; a rotation or restart that no longer
  /// starts with this snapshot resets instead of hiding the new logs.
  private var clearedLogSnapshots = [String: [String]]()
  private var importedBackupFileName: String?
  private var presentedInspectionID: String?
  private var didResetFirstRunOnboarding = false

  override var isProduction: Bool { true }

  override var currentLiveLogins: [DesignLiveLogin] {
    store.accountProviders.filter { $0.livePresent == true }.map { source in
      let appID = source.clientId ?? source.providerId
      return DesignLiveLogin(
        providerID: source.providerId,
        sourceID: source.sourceId,
        providerName: nativeProviderName(source.providerId),
        appName: source.clientId.map(anypickDisplayName) ?? source.label,
        identity: source.liveIdentity,
        symbol: appSymbol(appID)
      )
    }
  }

  init(store: TrayStore) {
    self.store = store
    super.init(
      destination: .overview,
      phase: .loading,
      appearance: .system,
      activityShowsLogs: false,
      apps: [],
      accounts: [],
      proxies: [],
      activity: [],
      backups: []
    )
    storeObserver = store.objectWillChange.sink { [weak self] _ in
      // The @MainActor-isolated synchronize() cannot be called from a
      // non-isolated DispatchQueue.main.async closure under Swift 5.10+
      // actor isolation checking: the closure runs on the main thread but is
      // not main-actor-typed, so the runtime can silently drop the call and
      // the SwiftUI binding never sees the new state. A Task hopped onto
      // MainActor is the documented way and matches how SwiftUI itself
      // dispatches update notifications.
      Task { @MainActor [weak self] in self?.synchronize() }
    }
    synchronize()
  }

  func consume(_ data: Data) {
    store.consume(data)
  }

  override func refresh() {
    // From terminal-degraded, refresh must relaunch the service: emitting a
    // command into a dead pipe is a no-op, which is why Try Again did nothing.
    if case .degraded = store.serviceState {
      store.restartService?()
      return
    }
    store.refreshAll()
  }

  override func dismissFirstRunWelcome() {
    TrayPreferences.firstRunWelcomeSeen = true
    super.dismissFirstRunWelcome()
  }

  override func detectSavedAccount(provider: String) -> Bool {
    guard let source = accountProvider(for: provider) else {
      showFeedback("No installed login source is available for \(provider).", kind: .error)
      return false
    }
    guard
      let requestID = store.mutate(
        operation: "account-detect",
        name: "detect",
        providerId: source.providerId,
        sourceId: source.sourceId,
        activityLabel: "detecting \(source.label) login"
      )
    else { return false }
    pendingOperations[requestID] = "account-detect"
    operationResult = nil
    return true
  }

  override func loginSavedAccount(
    provider: String,
    name: String,
    label: String,
    methodId: String
  ) -> Bool {
    guard let source = accountProvider(for: provider) else {
      showFeedback("No browser login source is available for \(provider).", kind: .error)
      return false
    }
    guard source.loginMethods?.contains(where: { $0.id == methodId && $0.available }) == true else {
      showFeedback("That browser login method is no longer available.", kind: .error)
      return false
    }
    let accountName = name.trimmingCharacters(in: .whitespacesAndNewlines)
    guard
      let requestID = store.mutate(
        operation: "account-login",
        name: accountName.isEmpty ? nil : accountName,
        providerId: source.providerId,
        sourceId: source.sourceId,
        methodId: methodId,
        label: label,
        activityLabel: "signing in to \(source.label)"
      )
    else { return false }
    pendingOperations[requestID] = "account-login"
    operationResult = nil
    return true
  }

  override func cancelAccountLogin() {
    guard
      let requestID = pendingOperations.first(where: { $0.value == "account-login" })?.key
    else { return }
    store.cancelMutation(requestId: requestID)
  }

  /// Typed credential save for compatible vendors (API keys / plan keys).
  /// The secret travels in the mutation's apiKey field — the same field
  /// gateway-create already uses for profile secrets.
  override func saveCredentialInput(
    provider: String,
    name: String,
    kind: String,
    profile: String,
    secret: String
  ) -> Bool {
    guard let source = accountProvider(for: provider) else {
      showFeedback("No credential source is available for \(provider).", kind: .error)
      return false
    }
    let kinds = source.credentialKinds ?? []
    guard kinds.contains(where: { $0.kind == kind }) else {
      showFeedback("\(source.label) does not accept that credential type.", kind: .error)
      return false
    }
    let accountName = name.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !accountName.isEmpty, !secret.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    else { return false }
    guard
      let requestID = store.mutate(
        operation: "account-save-input",
        name: accountName,
        providerId: source.providerId,
        apiKey: secret,
        kind: kind,
        profile: profile.isEmpty ? nil : profile,
        activityLabel: "saving \(source.label) account"
      )
    else { return false }
    pendingOperations[requestID] = "account-save-input"
    operationResult = nil
    return true
  }

  override func clearLiveLogin(provider: String) {
    guard let source = accountProvider(for: provider) else {
      showFeedback("No live login source is available for \(provider).", kind: .error)
      return
    }
    _ = store.mutate(
      operation: "account-clear",
      name: "live",
      providerId: source.providerId,
      sourceId: source.sourceId,
      activityLabel: "backing up and clearing \(source.label) login"
    )
  }

  override func testGateway(provider: String, endpoint: String, apiKey: String) -> Bool {
    guard let gateway = gatewayProvider(for: provider) else {
      showFeedback("Unknown gateway provider \(provider).", kind: .error)
      return false
    }
    guard
      let requestID = store.mutate(
        operation: "gateway-test",
        name: "test",
        providerId: gateway.id,
        endpoint: endpoint,
        apiKey: apiKey,
        activityLabel: "testing \(gateway.label) connection"
      )
    else { return false }
    pendingOperations[requestID] = "gateway-test"
    operationResult = nil
    return true
  }

  override func setRoute(appID: String, routeID: String) {
    guard let action = store.actions.first(where: { $0.id == routeID && $0.clientId == appID })
    else {
      showFeedback("That route is no longer available. Refresh and try again.", kind: .error)
      return
    }
    let app = apps.first { $0.id == appID }
    let from = app?.routeOptions.first { $0.id == app?.selectedRouteID }?.name ?? ""
    // invoke() refuses while another mutation is in flight (nil): only enter
    // inFlight when the request was actually accepted, or the sheet hangs in
    // Applying… with no result ever arriving (A1).
    guard action.enabled else { return }
    guard store.invoke(actionId: action.id, label: action.label) != nil else { return }
    switchPhase = .inFlight(appID: appID, from: from, to: action.label, routeID: routeID)
  }

  @discardableResult
  override func updateAppConfiguration(
    appID: String,
    routeID: String,
    fastModel: String,
    reasoningModel: String,
    useCompatibleFallback: Bool
  ) -> Bool {
    guard let app = apps.first(where: { $0.id == appID }) else { return false }
    if routeID != app.selectedRouteID {
      // Keep the model edit across the switch: stash it for
      // flushPendingModelEdits once the snapshot confirms the new route.
      guard store.busyRequestId == nil else { return false }
      pendingConfigEdits[appID] = (routeID, fastModel, reasoningModel, useCompatibleFallback)
      setRoute(appID: appID, routeID: routeID)
      guard isSwitching(appID) else {
        pendingConfigEdits.removeValue(forKey: appID)
        return false
      }
      return true
    }
    return sendConfigEdit(
      app: app,
      fastModel: fastModel,
      reasoningModel: reasoningModel,
      useCompatibleFallback: useCompatibleFallback
    )
  }

  /// Resolve role names against the current route catalog and send them.
  /// Tracked so sheets can wait for the result instead of dismissing blind.
  @discardableResult
  private func sendConfigEdit(
    app: DesignApp,
    fastModel: String,
    reasoningModel: String,
    useCompatibleFallback: Bool
  ) -> Bool {
    guard let config = store.clientModelConfigs.first(where: { $0.clientId == app.id }),
      config.editable
    else {
      showFeedback("Model roles are managed by the selected app route.", kind: .information)
      return false
    }
    var roles = [String: String]()
    func actionId(for model: String) -> String? {
      let cleaned = model.trimmingCharacters(in: .whitespacesAndNewlines)
      guard !cleaned.isEmpty, cleaned != "auto" else { return nil }
      return config.options.first { $0.modelId == cleaned }?.actionId
    }
    let defaultModel = reasoningModel == "auto" ? fastModel : reasoningModel
    if let id = actionId(for: defaultModel) { roles["default"] = id }
    if let id = actionId(for: fastModel) { roles["haiku"] = id }
    if let id = actionId(for: reasoningModel) { roles["opus"] = id }
    guard roles["default"] != nil else {
      showFeedback("Choose a model published by the current route.", kind: .error)
      return false
    }
    guard
      let requestID = store.applyModelRoles(
        clientId: app.id, roleActionIds: roles, label: app.name)
    else { return false }
    pendingOperations[requestID] = "app-model-roles"
    operationResult = nil
    return true
  }

  @discardableResult
  override func updateAppModelRoles(
    appID: String,
    routeID: String,
    modelRoles: [String: String]
  ) -> Bool {
    guard let app = apps.first(where: { $0.id == appID }) else { return false }
    if routeID != app.selectedRouteID {
      // Keep the model edit across the switch: stash it for
      // flushPendingModelEdits once the snapshot confirms the new route.
      guard store.busyRequestId == nil else { return false }
      pendingModelRoleEdits[appID] = (routeID, modelRoles)
      setRoute(appID: appID, routeID: routeID)
      guard isSwitching(appID) else {
        pendingModelRoleEdits.removeValue(forKey: appID)
        return false
      }
      return true
    }
    return sendModelRolesEdit(app: app, modelRoles: modelRoles)
  }

  /// Resolve role names against the current route catalog and send them.
  /// Tracked so sheets can wait for the result instead of dismissing blind.
  @discardableResult
  private func sendModelRolesEdit(app: DesignApp, modelRoles: [String: String]) -> Bool {
    guard let config = store.clientModelConfigs.first(where: { $0.clientId == app.id }),
      config.editable
    else {
      showFeedback("Model roles are managed by the selected app route.", kind: .information)
      return false
    }
    var actionIDs = [String: String]()
    for (roleID, modelID) in modelRoles where !modelID.isEmpty {
      if let option = config.options.first(where: { $0.modelId == modelID }) {
        actionIDs[roleID] = option.actionId
      }
    }
    guard actionIDs["default"] != nil else {
      showFeedback("Choose a default model published by the current route.", kind: .error)
      return false
    }
    guard
      let requestID = store.applyModelRoles(
        clientId: app.id, roleActionIds: actionIDs, label: app.name)
    else { return false }
    pendingOperations[requestID] = "app-model-roles"
    operationResult = nil
    return true
  }

  override func resetAppConfiguration(appID: String) {
    _ = store.mutate(
      operation: "client-reset",
      name: appID,
      activityLabel: "resetting \(anypickDisplayName(appID)) defaults"
    )
  }

  override func addSavedAccount(
    provider: String,
    name: String,
    label: String,
    identity: String
  ) -> Bool {
    guard let source = accountProvider(for: provider) else {
      showFeedback("No supported login source is available for \(provider).", kind: .error)
      return false
    }
    // Tracked (C4): the sheet stays open until the result arrives instead of
    // dismissing on accept and losing field context on failure.
    guard
      let requestID = store.mutate(
        operation: "account-save",
        name: name,
        providerId: source.providerId,
        sourceId: source.sourceId,
        label: label,
        activityLabel: "saving \(source.label) account"
      )
    else { return false }
    pendingOperations[requestID] = "account-save"
    operationResult = nil
    return true
  }

  override func saveCurrentLogin(_ login: DesignLiveLogin) -> Bool {
    guard
      store.accountProviders.contains(where: {
        $0.providerId == login.providerID
          && $0.sourceId == login.sourceID
          && $0.livePresent == true
      })
    else {
      showFeedback("That local login is no longer available. Refresh and try again.", kind: .error)
      return false
    }
    guard
      let requestID = store.mutate(
        operation: "account-save-current",
        providerId: login.providerID,
        sourceId: login.sourceID,
        activityLabel: "saving the current \(login.appName) login"
      )
    else { return false }
    pendingOperations[requestID] = "account-save-current"
    operationResult = nil
    return true
  }

  override func addGateway(
    provider: String,
    name: String,
    label: String,
    endpoint: String,
    apiKey: String,
    region: String?,
    defaultModel: String
  ) -> Bool {
    guard let gateway = gatewayProvider(for: provider) else {
      showFeedback("Unknown gateway provider \(provider).", kind: .error)
      return false
    }
    let operation = gateway.kind == "account-api-key" ? "account-save" : "gateway-create"
    guard
      let requestID = store.mutate(
        operation: operation,
        name: name,
        providerId: gateway.id,
        label: label,
        endpoint: endpoint,
        apiKey: apiKey,
        region: region,
        defaultModel: defaultModel,
        activityLabel: "adding \(gateway.label)"
      )
    else { return false }
    if operation == "gateway-create" {
      // Arm the post-create auto-connect only when the engine reports
      // success (A9): arming on accept lets a failed create capture the next
      // same-named gateway — and "Production" is the default name.
      pendingOperations[requestID] = operation
      pendingGatewayNames[requestID] = name
      operationResult = nil
    }
    return true
  }

  @discardableResult
  override func updateAccount(id: String, label: String) -> Bool {
    // Tracked (C4) so the edit sheet can wait for the result. The store
    // refuses while another mutation is in flight (nil request): report it so
    // the sheet only waits for a result that will actually arrive (A1).
    if let account = store.accounts.first(where: { $0.id == id }) {
      guard
        let requestID = store.mutate(
          operation: "account-edit",
          name: account.name,
          providerId: account.providerId,
          label: label,
          activityLabel: "updating \(account.label)"
        )
      else { return false }
      pendingOperations[requestID] = "account-edit"
      operationResult = nil
      return true
    } else if let gateway = gatewaySnapshot(for: id) {
      guard
        let requestID = store.mutate(
          operation: "gateway-edit",
          name: gateway.name,
          providerId: gateway.providerId,
          label: label,
          activityLabel: "updating \(gateway.name)"
        )
      else { return false }
      pendingOperations[requestID] = "gateway-edit"
      operationResult = nil
      return true
    }
    return false
  }

  override func refreshAccount(id: String) {
    // Tracked (C4) for an honest result trail; the row has no sheet to hold,
    // so failures surface through the result toast like other row actions.
    if let account = store.accounts.first(where: { $0.id == id }) {
      guard
        let requestID = store.mutate(
          operation: "account-refresh",
          name: account.name,
          providerId: account.providerId,
          activityLabel: "refreshing \(account.label)"
        )
      else { return }
      pendingOperations[requestID] = "account-refresh"
      operationResult = nil
    } else if let gateway = gatewaySnapshot(for: id) {
      guard
        let requestID = store.mutate(
          operation: "gateway-refresh",
          name: gateway.name,
          providerId: gateway.providerId,
          activityLabel: "refreshing \(gateway.name) models"
        )
      else { return }
      pendingOperations[requestID] = "gateway-refresh"
      operationResult = nil
    }
  }

  /// Re-probe a saved gateway with its stored credential (C7). The key never
  /// leaves the engine: the frame carries only the gateway name, and
  /// handleGatewayTest resolves the profile server-side.
  override func testSavedGateway(id: String) {
    guard let gateway = gatewaySnapshot(for: id) else {
      showFeedback(
        "That gateway is no longer available. Refresh and try again.", kind: .error)
      return
    }
    guard
      let requestID = store.mutate(
        operation: "gateway-test",
        name: gateway.name,
        providerId: gateway.providerId,
        activityLabel: "testing \(gateway.name) connection"
      )
    else { return }
    pendingOperations[requestID] = "gateway-test"
    operationResult = nil
  }

  override func makeAccountCurrent(id: String) {
    guard let account = store.accounts.first(where: { $0.id == id }) else {
      showFeedback(
        "That saved account is no longer available. Refresh and try again.", kind: .error)
      return
    }
    switchingAccountID = id
    guard
      let requestID = store.invoke(
        actionId: account.switchActionId,
        label: "make \(account.label) current"
      )
    else {
      switchingAccountID = nil
      return
    }
    pendingOperations[requestID] = "account-switch"
    operationResult = nil
  }

  override func connectGateway(id: String, appIDs: Set<String>) {
    let changes = gatewayRouteChanges(id: id, appIDs: appIDs)
    guard !changes.isEmpty else {
      showFeedback("No app routes needed to change.", kind: .information)
      return
    }
    pendingRouteChanges = changes
    applyNextPendingRouteChange()
  }

  private func applyNextPendingRouteChange() {
    guard let next = pendingRouteChanges.first else { return }
    pendingRouteChanges.removeFirst()
    setRoute(appID: next.appID, routeID: next.routeID)
  }

  /// Apply model edits stashed across a route change once the snapshot
  /// confirms the new route. A stash whose route never confirms waits for
  /// the switch result (failure drops it above), so this never re-sends a
  /// stale edit against the wrong catalog.
  private func flushPendingModelEdits() {
    guard store.busyRequestId == nil else { return }
    for (appID, edit) in pendingModelRoleEdits {
      guard !isSwitching(appID),
        !pendingRouteChanges.contains(where: { $0.appID == appID })
      else { continue }
      guard let app = apps.first(where: { $0.id == appID }) else {
        pendingModelRoleEdits.removeValue(forKey: appID)
        continue
      }
      guard app.selectedRouteID == edit.routeID else { continue }
      pendingModelRoleEdits.removeValue(forKey: appID)
      sendModelRolesEdit(app: app, modelRoles: edit.roles)
    }
    for (appID, edit) in pendingConfigEdits {
      guard !isSwitching(appID),
        !pendingRouteChanges.contains(where: { $0.appID == appID })
      else { continue }
      guard let app = apps.first(where: { $0.id == appID }) else {
        pendingConfigEdits.removeValue(forKey: appID)
        continue
      }
      guard app.selectedRouteID == edit.routeID else { continue }
      pendingConfigEdits.removeValue(forKey: appID)
      sendConfigEdit(
        app: app,
        fastModel: edit.fast,
        reasoningModel: edit.reasoning,
        useCompatibleFallback: edit.fallback
      )
    }
  }

  override func removeAccount(id: String) {
    // Tracked (C4) for the result trail; the confirmation dialog still
    // closes at once — there is no field context to preserve, and the
    // result toast reports failures like every other destructive row action.
    if let account = store.accounts.first(where: { $0.id == id }) {
      guard
        let requestID = store.mutate(
          operation: "account-remove",
          name: account.name,
          providerId: account.providerId,
          activityLabel: "removing \(account.label)"
        )
      else { return }
      pendingOperations[requestID] = "account-remove"
      operationResult = nil
    } else if let gateway = gatewaySnapshot(for: id) {
      guard
        let requestID = store.mutate(
          operation: "gateway-remove",
          name: gateway.name,
          providerId: gateway.providerId,
          activityLabel: "removing \(gateway.name)"
        )
      else { return }
      pendingOperations[requestID] = "gateway-remove"
      operationResult = nil
    }
  }

  override func setHubSource(_ accountID: String, enabled: Bool) {
    guard let source = store.hubSources.first(where: { $0.id == accountID }) else { return }
    // Contract with the engine (hub_snapshot.go): Name = hub, SourceID =
    // account. The old call sent the account label as Name, which the engine
    // read as the hub name and answered HUB_NOT_FOUND (H2).
    guard
      let requestID = store.mutate(
        operation: "hub-source-toggle",
        name: source.hub ?? "default",
        providerId: source.providerId,
        sourceId: source.name,
        enabled: enabled,
        activityLabel: "updating Proxy Hub sources"
      )
    else { return }
    // Row-level progress/failure like proxy toggles (H7): the requestID used
    // to be discarded, so toggle errors only reached a generic toast.
    let serviceID = hubSourceServiceID(accountID)
    let operation: DesignServiceOperation = enabled ? .starting : .stopping
    trackServiceOperation(operation, for: serviceID)
    serviceFailures.removeValue(forKey: serviceID)
    hubSourcePending[serviceID] = enabled
    serviceRequests[requestID] = (serviceID, operation)
  }

  override func selectRoutingCandidate(actionID: String) {
    guard let requestID = store.invoke(actionId: actionID, label: "select Proxy Hub source")
    else { return }
    let conflictID = routingConflicts.first(where: {
      $0.candidates.contains(where: { $0.actionID == actionID })
    })?.id
    let serviceID = hubConflictServiceID(conflictID ?? actionID)
    trackServiceOperation(.starting, for: serviceID)
    serviceFailures.removeValue(forKey: serviceID)
    serviceRequests[requestID] = (serviceID, .starting)
  }

  override func setProxyEnabled(id: String, enabled: Bool) {
    guard let proxy = store.proxies.first(where: { $0.id == id }) else { return }
    let actionId = proxy.toggleActionId
    guard
      let requestID = store.invoke(
        actionId: actionId,
        label: enabled ? "start \(proxy.label)" : "stop \(proxy.label)"
      )
    else { return }
    let serviceID = designServiceID(proxy)
    let operation: DesignServiceOperation = enabled ? .starting : .stopping
    trackServiceOperation(operation, for: serviceID)
    serviceFailures.removeValue(forKey: serviceID)
    serviceRequests[requestID] = (serviceID, operation)
  }

  override func setProxyHubEnabled(_ enabled: Bool) {
    guard let hub = store.proxyHub else {
      showFeedback("Configure Proxy Hub before starting it.", kind: .warning)
      return
    }
    setProxyEnabled(id: hub.id, enabled: enabled)
  }

  override func restartProxy(id: String) {
    let proxy = id == "hub" ? store.proxyHub : store.proxies.first(where: { $0.id == id })
    guard let proxy else { return }
    guard
      let requestID = store.invoke(
        actionId: proxy.restartActionId,
        label: "restart \(proxy.label)"
      )
    else { return }
    let serviceID = designServiceID(proxy)
    trackServiceOperation(.restarting, for: serviceID)
    serviceFailures.removeValue(forKey: serviceID)
    serviceRequests[requestID] = (serviceID, .restarting)
  }

  override func restartRunningProxies() {
    store.restartProxies()
  }

  override func stopAllProxies() {
    store.stopProxies()
  }

  override func createBackup(accountIDs: Set<String>) -> Bool {
    let selected = store.accounts.filter { accountIDs.contains($0.id) }
    guard !selected.isEmpty else {
      showFeedback("Select at least one saved account.", kind: .error)
      return false
    }
    let panel = NSSavePanel()
    panel.allowedContentTypes = [anyPickBackupType]
    panel.nameFieldStringValue = "anypick-accounts.anypick-backup"
    panel.canCreateDirectories = true
    guard panel.runModal() == .OK, let path = panel.url?.path else { return false }
    guard
      let requestID = store.exportBackup(
        path: path,
        accounts: selected.map { BackupAccountRef(provider: $0.providerId, name: $0.name) },
        overwrite: true,
        activityLabel: "exporting account backup"
      )
    else { return false }
    pendingOperations[requestID] = "account-backup-export"
    operationResult = nil
    return true
  }

  override func importBackupPackage() -> Bool {
    let panel = NSOpenPanel()
    panel.allowedContentTypes = [anyPickBackupType]
    panel.allowsMultipleSelection = false
    panel.canChooseDirectories = false
    panel.canChooseFiles = true
    panel.message = "Choose an AnyPick account backup to review."
    panel.prompt = "Choose"
    guard panel.runModal() == .OK, let path = panel.url?.path else { return false }
    importedBackupFileName = URL(fileURLWithPath: path).lastPathComponent
    return store.inspectBackup(path: path) != nil
  }

  override func restoreBackup(id: String, accountIDs: Set<String>, overwrite: Bool) {
    guard let preview = store.backupPreview,
      preview.inspectionId == id,
      let entries = preview.entries
    else {
      // Stale sheet: the review is gone (forgotten, expired, or superseded).
      showFeedback(
        "That review is no longer available. Choose the backup file again.", kind: .error)
      return
    }
    let selected = entries.filter { accountIDs.contains($0.id) }
    let dispositions = Dictionary(uniqueKeysWithValues: entries.map { ($0.id, $0.disposition) })
    let entryIds = backupImportEntryIDs(
      selectedIDs: Set(selected.map(\.id)),
      dispositions: dispositions,
      overwrite: overwrite
    )
    guard !entryIds.isEmpty else {
      // Skip-with-all-replace used to sit open with no feedback (B7).
      showFeedback(
        "Nothing to import: every selected account is already saved. Choose Replace to overwrite.",
        kind: .information
      )
      return
    }
    guard
      let requestID = store.importBackup(
        inspectionId: id,
        entryIds: entryIds,
        overwriteConfirmed: overwrite,
        activityLabel: "importing account backup"
      )
    else { return }
    pendingOperations[requestID] = "account-backup-import"
    operationResult = nil
    backupInspectionState = .importing
    backupInspectionMessage = "Importing the reviewed accounts…"
  }

  @discardableResult
  override func deleteBackup(id: String) -> Bool {
    // The staged server copy clears only after the engine confirms (B3): the
    // forget travels on the single-flight slot, so clearing on send dropped
    // the row while the credentials stayed staged on a refusal or failure.
    guard store.backupPreview?.inspectionId == id else {
      backups.removeAll { $0.id == id }
      return true
    }
    guard let requestID = store.forgetBackup(inspectionId: id) else { return false }
    pendingOperations[requestID] = "account-backup-forget"
    pendingForgetBackupIDs[requestID] = id
    operationResult = nil
    return true
  }

  override func clearActivity() {
    if activityShowsLogs, let source = selectedLogSource,
      let text = store.logsBySource[source.id]
    {
      // Snapshot the cleared tail itself (not its length): rotation or a
      // restart replaces the content, which a count could never detect.
      clearedLogSnapshots[source.id] = text.split(
        separator: "\n",
        omittingEmptySubsequences: false
      ).map(String.init)
    } else {
      clearedActivityIDs.formUnion(store.activity.map(\.id))
    }
    activity = designActivity()
    showFeedback("Cleared the visible activity list. Supervisor history is unchanged.")
  }

  override var hasInstalledApp: Bool { store.hasInstalledClient }

  override var hasChosenRoute: Bool { overviewHasChosenRoute(apps: apps) }

  override var isFirstRun: Bool { store.isFirstRun }

  override func dismissOnboarding() {
    super.dismissOnboarding()
    TrayPreferences.onboardingDismissed = true
  }

  override func showGettingStartedAgain() {
    TrayPreferences.onboardingDismissed = false
    super.showGettingStartedAgain()
  }

  override func setOpenAtLogin(_ enabled: Bool) {
    sendSetting(key: "openAtLogin", changed: store.settings.launchAtLogin != enabled) {
      store.mutate(
        operation: "setting-launch-at-login",
        name: "settings",
        enabled: enabled,
        activityLabel: "updating login settings"
      )
    }
  }

  override func setShowUsage(_ enabled: Bool) {
    sendSetting(key: "showUsage", changed: store.settings.showQuota != enabled) {
      store.mutate(
        operation: "setting-show-quota",
        name: "settings",
        enabled: enabled,
        activityLabel: "updating quota visibility"
      )
    }
  }

  override func setStartProxiesAtLaunch(_ enabled: Bool) {
    sendSetting(key: "startProxiesAtLaunch", changed: store.settings.startEnabledProxies != enabled)
    {
      store.mutate(
        operation: "setting-auto-start-proxies",
        name: "settings",
        enabled: enabled,
        activityLabel: "updating proxy auto-start"
      )
    }
  }

  override func setWarnBeforeLowUsage(_ enabled: Bool) {
    sendSetting(key: "warnBeforeLowUsage", changed: store.settings.quotaGuardEnabled != enabled) {
      store.mutate(
        operation: "setting-quota-guard",
        name: "settings",
        enabled: enabled,
        activityLabel: "updating Quota Guard"
      )
    }
  }

  override func setActivityShowsLogs(_ enabled: Bool) {
    // The Events/Live Logs segmented binding already wrote the value before
    // onChange re-enters here; @Published fires on every set even when equal,
    // and each fire rebuilds the pane. Only publish on a real toggle.
    if activityShowsLogs != enabled {
      activityShowsLogs = enabled
    }
    if enabled, let source = selectedLogSource {
      store.selectLogSource(source.id)
      store.requestLogs(source)
    }
  }

  override func selectLogSource(_ id: String) {
    guard let source = store.logSources.first(where: { $0.id == id }) else { return }
    if selectedLogSourceID != id {
      selectedLogSourceID = id
    }
    store.selectLogSource(id)
    store.requestLogs(source)
  }

  override func refreshSelectedLogSource() {
    guard let source = selectedLogSource else { return }
    store.requestLogs(source)
  }

  override func setLogPolling(_ enabled: Bool) {
    guard let source = selectedLogSource else {
      store.stopLogPolling()
      return
    }
    if enabled {
      store.startLogPolling(for: source.id)
    } else {
      store.stopLogPolling()
    }
  }

  private func synchronizeOnboarding() {
    guard store.snapshotReady else { return }
    if store.isFirstRun {
      if !didResetFirstRunOnboarding {
        didResetFirstRunOnboarding = true
        TrayPreferences.resetOnboardingIfEmpty(isFirstRun: true)
        onboardingDismissed = false
      }
      showFirstRunWelcome = !TrayPreferences.firstRunWelcomeSeen
      return
    }
    showFirstRunWelcome = false
    didResetFirstRunOnboarding = false
    if !showGettingStarted {
      onboardingDismissed = TrayPreferences.onboardingDismissed
    }
  }

  private func synchronize() {
    // A degraded service with data stays on Overview (O5): its Needs
    // Attention block names the condition. .error is only for having nothing
    // to show at all — the generic pane it renders hides that block.
    if case .degraded = store.serviceState {
      phase = store.snapshotReady ? .ready : .error
    } else {
      phase = store.snapshotReady ? .ready : .loading
    }
    degradedConditions = store.degradedConditions.map {
      DesignDegradedCondition(id: $0.id, kind: $0.kind, title: $0.title, detail: $0.detail)
    }
    apps = store.managedAppClientIDs.compactMap(designApp).sorted(by: nativeAppOrder)
    accounts = (store.accounts.map(designAccount) + store.gateways.map(designGateway))
      .sorted(by: nativeAccountOrder)
    if let name = pendingGatewayConnectName,
      let created = accounts.first(where: {
        $0.kind == .gateway && $0.name.caseInsensitiveCompare(name) == .orderedSame
      })
    {
      pendingGatewayConnectID = created.id
      pendingGatewayConnectName = nil
    }
    // Hub is presented beside these services by the macOS view, so keep it out
    // of the per-account snapshot list to avoid rendering it twice.
    proxies = store.proxies.filter { $0.providerId != "proxy-hub" }.map(designProxy)
    hubSources = store.hubSources.map(designHubSource)
    hubSourceIDs = Set(store.hubSources.filter(\.enabled).map(\.id))
    // Catalogs (static policy + warmed live lists, pins merged server-side)
    // compile to counts, conflicts and published models here — one logic for
    // fixture and production. The legacy Go-computed conflicts/published
    // stay on the wire but no longer drive the pane.
    hubCatalogs = store.hubCatalogs.map {
      DesignHubCatalog(accountID: $0.accountID, catalogID: $0.catalog, models: $0.models)
    }
    recompileHubRouting()
    proxyHubEnabled = store.proxyHub?.running == true
    proxyHubPort = servicePort(from: store.proxyHub?.address) ?? proxyHubPort
    logSources = store.logSources.map {
      DesignLogSource(
        id: $0.id,
        label: $0.label,
        detail: $0.detail,
        provider: $0.providerId,
        symbol: providerSymbol($0.providerId)
      )
    }
    // The store re-emits the full snapshot on every poll. @Published fires on
    // every assignment even when unchanged, rebuilding the Activity list and
    // re-laying out the pane — the visible flicker. Only publish on change.
    // (selectLogSource sets both sides synchronously, so a plain mirror here
    // cannot fight the user's just-made choice.)
    let resolvedLogSourceID = selectedLogSource?.id ?? ""
    if selectedLogSourceID != resolvedLogSourceID {
      selectedLogSourceID = resolvedLogSourceID
    }
    synchronizeLogState()
    // In-flight settings keep their local value (S3): rewriting the stale
    // snapshot value until the supervisor acks reads as flicker.
    if !pendingSettings.contains("openAtLogin") { openAtLogin = store.settings.launchAtLogin }
    if !pendingSettings.contains("showUsage") { showUsage = store.settings.showQuota }
    if !pendingSettings.contains("startProxiesAtLaunch") {
      startProxiesAtLaunch = store.settings.startEnabledProxies
    }
    if !pendingSettings.contains("warnBeforeLowUsage") {
      warnBeforeLowUsage = store.settings.quotaGuardEnabled
    }
    let wasBusy = isBusy
    isBusy = store.busyRequestId != nil
    if wasBusy, !isBusy {
      switchingAccountID = nil
      // Backstop for pendingSettings (S3): the loop is idle, so any setting
      // still held either applied or was dropped — sync resumes either way.
      pendingSettings.removeAll()
      if !pendingRouteChanges.isEmpty {
        applyNextPendingRouteChange()
      }
    }
    if wasBusy, !isBusy, pendingRouteChanges.isEmpty,
      case .inFlight(let appID, let from, let to, let routeID) = switchPhase,
      let result = store.lastResult
    {
      if result.status == "success" {
        switchPhase = .success(appID: appID, name: to)
        Task { @MainActor in
          try? await Task.sleep(nanoseconds: 1_600_000_000)
          if case .success = switchPhase { switchPhase = .idle }
        }
      } else {
        switchPhase = .recovery(appID: appID, requested: to, kept: from, routeID: routeID)
        // The switch failed: a model edit stashed across it no longer
        // applies — say so instead of stranding it silently.
        if pendingModelRoleEdits.removeValue(forKey: appID) != nil
          || pendingConfigEdits.removeValue(forKey: appID) != nil
        {
          showFeedback(
            "The connection change failed. Model choices were not applied.",
            kind: .warning)
        }
      }
    }
    // @Published fires on every set even when the value is identical; each
    // fire rebuilds the Activity list and re-lays out the pane. Only publish
    // on real change so background polls with identical tails stay quiet.
    let freshLogLines = designLogLines()
    if liveLogLines != freshLogLines {
      liveLogLines = freshLogLines
    }
    let freshActivity = designActivity()
    if activity != freshActivity {
      activity = freshActivity
    }
    synchronizeOnboarding()
    synchronizeBackupPreview()
    synchronizeBackupInspectionState()
    confirmServiceOperations()
    flushPendingModelEdits()

    if let result = store.lastResult, result.requestId != lastResultId {
      lastResultId = result.requestId
      if let service = serviceRequests.removeValue(forKey: result.requestId) {
        if result.status == "success" {
          // Success only means the engine accepted the action. The spinner
          // stays until a snapshot confirms the target state
          // (confirmServiceOperations); otherwise a fast result flashes
          // "done" over a still-stopped proxy.
          serviceFailures.removeValue(forKey: service.id)
        } else {
          cancelServiceTimeout(for: service.id)
          serviceOperations.removeValue(forKey: service.id)
          hubSourcePending.removeValue(forKey: service.id)
          serviceFailures[service.id] =
            result.message ?? "The service could not complete that action."
        }
      }
      if let operation = pendingOperations.removeValue(forKey: result.requestId) {
        operationResult = DesignOperationResult(
          requestID: result.requestId,
          operation: operation,
          succeeded: result.status == "success",
          message: result.message ?? ""
        )
        if operation == "gateway-create" {
          // One-shot auto-connect arming (A9): only a successful create may
          // capture the next gateway of the same name.
          let name = pendingGatewayNames.removeValue(forKey: result.requestId)
          if result.status == "success", let name {
            pendingGatewayConnectName = name
          }
        }
        if operation.hasPrefix("setting-") {
          pendingSettings.remove(String(operation.dropFirst("setting-".count)))
        }
        if operation == "account-backup-import" {
          if result.status == "success" {
            backupInspectionState = .success
            backupInspectionMessage = result.message ?? "Accounts imported."
          } else if result.message?.localizedCaseInsensitiveContains("expired") == true {
            backupInspectionState = .expired
            backupInspectionMessage = result.message
          } else {
            backupInspectionState = .atomicFailure
            backupInspectionMessage = result.message ?? "No saved accounts were changed."
          }
        }
        if operation == "account-backup-forget" {
          // Server-confirmed forget (B3): only now drop the staged copy and
          // the row. Failures keep both; the generic feedback below surfaces
          // the engine reason as the error alert.
          let backupID = pendingForgetBackupIDs.removeValue(forKey: result.requestId)
          if result.status == "success", let backupID {
            if store.backupPreview?.inspectionId == backupID {
              store.clearBackupPreview()
            }
            backups.removeAll { $0.id == backupID }
          }
        }
      }
      showFeedback(
        result.message ?? (result.status == "success" ? "Action completed." : "Action failed."),
        kind: result.status == "success" ? .success : .error
      )
    }
  }

  private func designApp(clientID: String) -> DesignApp? {
    let actions = store.managedActions(clientId: clientID)
    let provider = store.accountProviders.first { $0.clientId == clientID && $0.installed }
    guard !actions.isEmpty || provider != nil else {
      // Actions exist but none are enabled (e.g. incompatible routes): show
      // an attention row with a fix action instead of dropping the app (A11).
      guard appNeedsAttentionPlaceholder(
        clientID: clientID,
        actions: store.actions,
        providers: store.accountProviders
      ) else { return nil }
      return DesignApp(
        id: clientID,
        name: designAppName(clientID),
        detail: "No usable connection",
        symbol: appSymbol(clientID),
        status: .attention,
        showInQuickPanel: false,
        accountCount: 0,
        selectedRouteID: "",
        routeOptions: [],
        fastModel: "auto",
        reasoningModel: "auto",
        useCompatibleFallback: true,
        attentionMessage: "No compatible login is available for this app."
      )
    }
    let route = store.route(clientId: clientID)
    let selected = actions.first(where: \.selected)
    let config = store.modelConfig(clientID)
    let options = uniqueCompatibleRoutes(
      actions.map {
        DesignRoute(
          id: $0.id,
          name: $0.routeKind == "direct-account"
            ? ($0.upstreamSourceLabel ?? $0.label)
            : $0.label,
          detail: friendlyRouteDetail($0.detail, routeKind: $0.routeKind),
          symbol: routeSymbol($0.routeKind),
          kind: appRouteKind(
            routeKind: $0.routeKind,
            upstreamProviderID: $0.upstreamProviderId,
            clientID: clientID
          ),
          providerID: $0.upstreamProviderId
        )
      },
      selectedID: selected?.id)
    let selectedName = selected.flatMap { action in
      options.first { $0.id == action.id }?.name
    }
    let routeStatus = route?.status.lowercased()
    let configuredRouteUnavailable =
      selected == nil
      && route?.source?.isEmpty == false
      && provider?.livePresent != true
    let status: DesignStatus =
      routeStatus == "attention"
      ? .attention
      : configuredRouteUnavailable
        ? .stopped
        : selected != nil || provider?.livePresent == true || routeStatus == "native"
          || routeStatus == "ready" ? .ready : .stopped
    let nativeLoginDetail: String? =
      provider?.livePresent == true
      ? (provider?.liveIdentity.map { "Signed in as \($0)" } ?? "Current login")
      : nil
    let connectionDetail =
      selectedName.map { "Uses \($0)" }
      ?? nativeLoginDetail
      ?? (route?.source?.isEmpty == false ? "Configured source unavailable" : nil)
      ?? "No account selected"
    let usageWindows =
      store.usage(for: clientID)?.windows.map {
        DesignUsageWindow(
          label: $0.label,
          remainingPercent: $0.remainingPercent,
          resetsAtMs: $0.resetsAtMs,
          limitWindowSeconds: $0.limitWindowSeconds
        )
      } ?? []
    return DesignApp(
      id: clientID,
      name: designAppName(clientID),
      detail: connectionDetail,
      symbol: appSymbol(clientID),
      status: status,
      showInQuickPanel: AppPreferences.quickPanelShown(for: clientID) ?? (selected != nil),
      accountCount: options.count,
      selectedRouteID: selected?.id ?? "",
      routeOptions: options,
      fastModel: config?.modelRoles["haiku"] ?? config?.defaultModel ?? "auto",
      reasoningModel: config?.modelRoles["opus"] ?? config?.defaultModel ?? "auto",
      useCompatibleFallback: true,
      modelRoles: config?.roles.map {
        DesignModelRole(
          id: $0.id,
          label: $0.label,
          modelID: config?.modelRoles[$0.id]
            ?? ($0.id == "default" ? config?.defaultModel ?? "" : "")
        )
      } ?? [],
      modelOptions: Array(Set(config?.options.map(\.modelId) ?? [])).sorted(),
      modelEditingAvailable: config?.editable ?? false,
      modelUnavailableReason: config?.unavailableReason,
      usageWindows: usageWindows,
      usageRemaining: usageWindows.first.map { "\($0.remainingPercent)% remaining" },
      attentionMessage: routeStatus == "attention" ? route?.attentionReason : nil
    )
  }

  private func designAccount(_ account: ManagedAccountSnapshot) -> DesignAccount {
    DesignAccount(
      id: account.id,
      name: account.label,
      detail: account.detail,
      provider: nativeProviderName(account.providerId),
      symbol: providerSymbol(account.providerId),
      kind: .saved,
      status: account.active ? .running : .ready,
      lastUsed: account.active ? "Used now" : "Saved"
    )
  }

  private func designGateway(_ gateway: GatewaySnapshot) -> DesignAccount {
    DesignAccount(
      id: "gateway/\(gateway.id)",
      name: gateway.name,
      detail: gateway.detail,
      provider: nativeProviderName(gateway.providerId),
      symbol: providerSymbol(gateway.providerId),
      kind: .gateway,
      status: gateway.ready ? .ready : .unavailable,
      lastUsed: gateway.ready ? "Available" : "Needs attention"
    )
  }

  private func designProxy(_ proxy: ProxySnapshot) -> DesignProxy {
    let required = proxyIsRequired(proxy)
    return DesignProxy(
      id: proxy.id,
      name: friendlyProxyName(label: proxy.label, providerId: proxy.providerId),
      detail: proxy.address ?? proxy.detail,
      symbol: providerSymbol(proxy.providerId),
      provider: proxy.providerId,
      status: proxy.running ? .running : proxy.enabled && required ? .attention : .stopped,
      port: servicePort(from: proxy.address) ?? 0,
      enabled: proxy.enabled
    )
  }

  private func proxyIsRequired(_ proxy: ProxySnapshot) -> Bool {
    let selectedByApp = store.actions.contains {
      $0.selected
        && $0.presentation == "app-route"
        && $0.upstreamProviderId == proxy.providerId
        && $0.routeKind != "direct-account"
    }
    let selectedByHub =
      store.proxyHub?.running == true
      && store.hubSources.contains { $0.enabled && $0.providerId == proxy.providerId }
    return selectedByApp || selectedByHub
  }

  // Activity is always the journal-backed event history. Live logs travel on
  // the separate liveLogLines path; mapping them into DesignActivity here used
  // index-based ids ("log-<index>-<hash>") that shifted on every appended
  // line, re-identifying the whole list on each 1.75s poll — the flicker.
  private func designActivity() -> [DesignActivity] {
    let formatter = DateFormatter()
    formatter.dateFormat = "HH:mm"
    return store.activity.filter { !clearedActivityIDs.contains($0.id) }.map {
      DesignActivity(
        id: $0.id,
        day: activityDayLabel($0.date),
        time: formatter.string(from: $0.date),
        title: $0.message,
        detail: activityDetail($0.kind, isError: $0.isError),
        symbol: activitySymbol($0.kind),
        status: $0.isError ? .attention : .ready,
        provider: $0.provider
      )
    }
  }

  private func activityDayLabel(_ date: Date) -> String {
    let calendar = Calendar.current
    if calendar.isDateInToday(date) { return "Today" }
    if calendar.isDateInYesterday(date) { return "Yesterday" }
    return date.formatted(date: .abbreviated, time: .omitted)
  }

  private var selectedLogSource: LogSourceSnapshot? {
    store.logSources.first { $0.id == store.selectedLogSourceId } ?? store.logSources.first
  }

  private func designLogLines() -> [String] {
    guard let source = selectedLogSource,
      let text = store.logsBySource[source.id], !text.isEmpty
    else { return [] }
    let lines = text.split(separator: "\n", omittingEmptySubsequences: false).map(String.init)
    // The cleared state is content-anchored: only hide while the tail still
    // starts with the cleared snapshot. Rotation or a restart replaces the
    // content — even at the same length — so reset and show the new logs
    // instead of hiding them behind a stale count.
    var hidden = 0
    if let snapshot = clearedLogSnapshots[source.id] {
      if lines.starts(with: snapshot) {
        hidden = snapshot.count
      } else {
        clearedLogSnapshots.removeValue(forKey: source.id)
      }
    }
    let visible = visibleLogLines(lines, hiddenCount: &hidden)
    return visible
  }

  private func synchronizeLogState() {
    // Every assignment below goes through setLogState so an unchanged
    // state/message pair stays quiet instead of rebuilding the pane.
    guard let source = selectedLogSource else {
      setLogState(
        .unavailable, message: "Choose another log source or return to Activity.")
      return
    }
    // Background polls set loadingLogSourceId on every 1.75s tick. Flipping
    // to .loading each time swaps the notice row (ProgressView in/out) and
    // hides the Live badge — the flicker. When a tail is already on screen,
    // stay .ready through the refresh; only the first load shows .loading.
    if store.loadingLogSourceId == source.id,
      store.logsBySource[source.id]?.isEmpty != false
    {
      setLogState(.loading, message: "Loading \(source.label) logs…")
      return
    }
    let serviceRunning: Bool
    if store.proxyHub?.id == source.id {
      serviceRunning = store.proxyHub?.running == true
    } else if let proxy = store.proxies.first(where: { $0.id == source.id }) {
      serviceRunning = proxy.running
    } else {
      // Orphaned source (service removed mid-flight, L3): never the idle
      // "Output appears here…" — the service is gone.
      setLogState(
        .unavailable, message: "This service is no longer available. Choose another log source."
      )
      return
    }
    if !serviceRunning {
      let startTarget = store.proxyHub?.id == source.id ? "Proxy Hub" : "the proxy"
      setLogState(
        .stopped,
        message: "Prior logs may remain visible. Start \(startTarget) to view new entries.")
      return
    }
    switch store.logStatesBySource[source.id] {
    case "error":
      setLogState(
        .error, message: "Showing the most recent logs that loaded successfully. Try again.")
    case "empty":
      setLogState(
        .empty, message: "The service is running but has not written any log entries.")
    case "ready":
      setLogState(.ready, message: nil)
    default:
      setLogState(
        store.logsBySource[source.id]?.isEmpty == false ? .ready : .idle, message: nil)
    }
  }

  /// @Published fires on every set even when the value is identical; route
  /// all log-state writes through here so steady states stay quiet.
  private func setLogState(_ state: DesignLogState, message: String?) {
    if logState != state {
      logState = state
    }
    if logStateMessage != message {
      logStateMessage = message
    }
  }

  private func synchronizeBackupInspectionState() {
    if store.backupPreviewRequestId != nil {
      backupInspectionState = .reading
      backupInspectionMessage = "Reading a protected review copy…"
      return
    }
    if backupInspectionState == .importing || backupInspectionState == .success
      || backupInspectionState == .expired || backupInspectionState == .atomicFailure
    {
      return
    }
    if let preview = store.backupPreview, preview.status == "error" {
      switch preview.reason {
      case "newer-version": backupInspectionState = .newerVersion
      case "permission-denied": backupInspectionState = .permissionDenied
      case "missing": backupInspectionState = .missing
      case "timeout": backupInspectionState = .damaged
      default: backupInspectionState = .damaged
      }
      backupInspectionMessage = preview.message ?? "Nothing was imported. Choose another file."
    } else if store.backupPreview?.status == "ready" {
      backupInspectionState = .ready
      backupInspectionMessage = nil
    } else {
      backupInspectionState = .idle
      backupInspectionMessage = nil
    }
  }

  private func designHubSource(_ source: HubSourceSnapshot) -> DesignHubSource {
    DesignHubSource(
      id: source.id,
      name: source.label,
      detail: source.detail,
      provider: nativeProviderName(source.providerId),
      symbol: providerSymbol(source.providerId),
      enabled: source.enabled,
      warning: source.warning,
      modelCount: source.modelCount ?? 0
    )
  }

  override var savedAccountProviderOptions: [DesignProviderOption] {
    store.accountProviders.map {
      DesignProviderOption(
        id: $0.id,
        label: $0.label,
        detail: Self.accountProviderDetail($0),
        enabled: $0.installed || ($0.loginMethods ?? []).contains(where: \.available)
          || !($0.credentialKinds ?? []).isEmpty,
        canClear: $0.canClear == true,
        loginMethods: ($0.loginMethods ?? []).map {
          DesignLoginMethod(
            id: $0.id,
            label: $0.label,
            detail: $0.detail ?? "Browser sign-in",
            provenance: $0.provenance,
            preferred: $0.preferred,
            available: $0.available,
            unavailableReason: $0.unavailableReason
          )
        },
        credentialKinds: ($0.credentialKinds ?? []).map {
          DesignCredentialKind(
            kind: $0.kind,
            label: $0.label,
            profiles: ($0.profiles ?? []).map {
              DesignCredentialProfile(id: $0.profileID, label: $0.label)
            }
          )
        }
      )
    }
  }

  override var gatewayProviderOptions: [DesignProviderOption] {
    store.gatewayProviders.map {
      DesignProviderOption(
        id: $0.id,
        label: $0.label,
        detail: $0.detail,
        defaultEndpoint: $0.defaultEndpoint,
        providesEndpoint: $0.providesEndpoint == true,
        requiresApiKey: $0.requiresApiKey == true,
        firstPartyClientIDs: $0.firstPartyClientIds ?? [],
        kind: $0.kind ?? "gateway",
        regions: $0.regions ?? [],
        defaultRegion: $0.regionDefault
      )
    }
  }

  private func synchronizeBackupPreview() {
    guard let preview = store.backupPreview,
      preview.status == "ready",
      let inspectionId = preview.inspectionId,
      let entries = preview.entries
    else { return }
    let previewAccounts = entries.map {
      DesignAccount(
        id: $0.id,
        name: $0.label,
        detail: $0.disposition == "replace" ? "Will replace saved account" : "Ready to import",
        provider: nativeProviderName($0.providerId),
        symbol: providerSymbol($0.providerId),
        kind: .saved,
        status: $0.disposition == "blocked" || $0.disposition == "unavailable"
          ? .unavailable : .ready,
        lastUsed: $0.disposition.capitalized
      )
    }
    backups = [
      DesignBackup(
        id: inspectionId,
        createdAt: preview.createdAt ?? "Imported backup",
        accountCount: previewAccounts.count,
        size: "Inspected package",
        location: importedBackupFileName ?? "Selected file",
        accounts: previewAccounts
      )
    ]
    if presentedInspectionID != inspectionId {
      presentedInspectionID = inspectionId
      pendingImportBackupID = inspectionId
    }
  }

  /// Tooltip for the provider picker. A row the user cannot act on keeps the
  /// engine's description, except when a sign-in method exists but its
  /// launcher is missing — then the picker says so instead of just dimming.
  private static func accountProviderDetail(_ source: AccountProviderSnapshot) -> String {
    let methods = source.loginMethods ?? []
    if !methods.isEmpty, !methods.contains(where: \.available), source.livePresent != true {
      if let reason = methods.compactMap(\.unavailableReason).first(where: { !$0.isEmpty }) {
        return reason
      }
    }
    return source.detail
  }

  private func accountProvider(for value: String) -> AccountProviderSnapshot? {
    store.accountProviders.first {
      $0.id.caseInsensitiveCompare(value) == .orderedSame
        || $0.providerId.caseInsensitiveCompare(value) == .orderedSame
        || $0.label.caseInsensitiveCompare(value) == .orderedSame
        || nativeProviderName($0.providerId).caseInsensitiveCompare(value) == .orderedSame
    }
  }

  private func gatewayProvider(for value: String) -> GatewayProviderSnapshot? {
    store.gatewayProviders.first {
      $0.id.caseInsensitiveCompare(value) == .orderedSame
        || $0.label.caseInsensitiveCompare(value) == .orderedSame
        || nativeProviderName($0.id).caseInsensitiveCompare(value) == .orderedSame
    }
  }

  private func gatewaySnapshot(for id: String) -> GatewaySnapshot? {
    let raw = id.hasPrefix("gateway/") ? String(id.dropFirst("gateway/".count)) : id
    return store.gateways.first { $0.id == raw || $0.name == raw }
  }

  private func designServiceID(_ proxy: ProxySnapshot) -> String {
    proxy.providerId == "proxy-hub" ? "hub" : proxy.id
  }

  /// How long a service spinner may wait for snapshot confirmation before
  /// failing loudly instead of spinning forever.
  private static let serviceConfirmTimeout: UInt64 = 15_000_000_000

  private func trackServiceOperation(_ operation: DesignServiceOperation, for serviceID: String) {
    serviceOperations[serviceID] = operation
    cancelServiceTimeout(for: serviceID)
    serviceTimeoutTasks[serviceID] = Task { @MainActor [weak self] in
      try? await Task.sleep(nanoseconds: Self.serviceConfirmTimeout)
      guard let self, !Task.isCancelled else { return }
      guard self.serviceOperations[serviceID] != nil else { return }
      self.serviceOperations.removeValue(forKey: serviceID)
      self.serviceFailures[serviceID] =
        "The service didn’t confirm in time. Check Live Logs for details."
      self.showFeedback(
        "The service didn’t confirm in time. Check Live Logs for details.",
        kind: .error)
    }
  }

  private func cancelServiceTimeout(for serviceID: String) {
    serviceTimeoutTasks.removeValue(forKey: serviceID)?.cancel()
  }

  /// Clears service spinners whose target state a snapshot now confirms.
  /// Runs on every synchronize (after proxies sync), so the spinner stops
  /// on `running`, not on the faster command-accepted result.
  private func confirmServiceOperations() {
    for (serviceID, operation) in serviceOperations {
      // Conflict resolve has no snapshot state to confirm against; the engine
      // result or the timeout settles it (H7).
      if serviceID.hasPrefix("hub-conflict/") { continue }
      // Hub-source toggles confirm against the snapshot's enabled flag, not
      // the faster command-accepted result (H7).
      if let desired = hubSourcePending[serviceID] {
        if serviceFailures[serviceID] != nil {
          hubSourcePending.removeValue(forKey: serviceID)
        } else if store.hubSources.first(where: { hubSourceServiceID($0.id) == serviceID })?
          .enabled == desired
        {
          cancelServiceTimeout(for: serviceID)
          serviceOperations.removeValue(forKey: serviceID)
          hubSourcePending.removeValue(forKey: serviceID)
        }
        continue
      }
      let running: Bool?
      if serviceID == "hub" {
        running = store.proxyHub?.running
      } else if let proxy = store.proxies.first(where: { $0.id == serviceID }) {
        running = proxy.running
      } else {
        running = nil
      }
      guard let running else {
        // Row vanished (account removed mid-flight): never strand a spinner.
        cancelServiceTimeout(for: serviceID)
        serviceOperations.removeValue(forKey: serviceID)
        continue
      }
      let confirmed =
        switch operation {
        case .starting, .restarting: running
        case .stopping: !running
        }
      if confirmed {
        cancelServiceTimeout(for: serviceID)
        serviceOperations.removeValue(forKey: serviceID)
      }
    }
  }

  private func providerSymbol(_ provider: String) -> String {
    switch provider.lowercased() {
    case "claude", "anthropic": return "sparkles"
    case "codex", "openai", "opencode": return "terminal"
    case "gemini", "google", "antigravity": return "diamond.fill"
    case "kiro": return "shippingbox.fill"
    case "proxy-hub": return "point.3.connected.trianglepath.dotted"
    case "workbuddy", "codebuddy": return "briefcase.fill"
    case "zcode": return "curlybraces"
    default: return "network"
    }
  }

  private func appSymbol(_ client: String) -> String { providerSymbol(client) }

  private func designAppName(_ client: String) -> String {
    switch client.lowercased() {
    case "claude": return "Claude Code"
    case "codex": return "Codex"
    case "gemini": return "Gemini"
    case "kiro": return "Kiro"
    case "grok": return "Grok"
    case "opencode": return "OpenCode"
    case "workbuddy", "codebuddy": return "WorkBuddy"
    case "zcode": return "ZCode"
    case "dsh": return "DeepSeek Harness"
    default: return anypickDisplayName(client)
    }
  }

  private func routeSymbol(_ kind: String?) -> String {
    switch kind {
    case "hub": return "point.3.connected.trianglepath.dotted"
    case "gateway": return "network"
    case "pool", "account": return "arrow.triangle.branch"
    default: return "person.crop.circle"
    }
  }

  private func activityDetail(_ kind: String, isError: Bool) -> String {
    switch kind {
    case "account": return isError ? "The saved login was not changed." : "Saved login updated."
    case "gateway": return isError ? "The saved gateway was not changed." : "Gateway updated."
    case "proxy": return isError ? "The service needs attention." : "Local service updated."
    case "quota": return isError ? "Usage could not be refreshed." : "Usage refreshed."
    case "settings": return isError ? "The setting was not changed." : "Setting updated."
    default: return isError ? "The previous app configuration was kept." : "App routing updated."
    }
  }

  private func nativeAppOrder(_ lhs: DesignApp, _ rhs: DesignApp) -> Bool {
    let priority = ["claude", "codex", "gemini", "kiro", "grok", "opencode", "workbuddy", "zcode"]
    let lhsIndex = priority.firstIndex(of: lhs.id) ?? priority.count
    let rhsIndex = priority.firstIndex(of: rhs.id) ?? priority.count
    if lhsIndex != rhsIndex { return lhsIndex < rhsIndex }
    return lhs.name.localizedStandardCompare(rhs.name) == .orderedAscending
  }

  private func nativeAccountOrder(_ lhs: DesignAccount, _ rhs: DesignAccount) -> Bool {
    if lhs.kind != rhs.kind { return lhs.kind == .saved }
    let lhsIndex = nativeProviderPriority(lhs.provider)
    let rhsIndex = nativeProviderPriority(rhs.provider)
    if lhsIndex != rhsIndex { return lhsIndex < rhsIndex }
    return lhs.name.localizedStandardCompare(rhs.name) == .orderedAscending
  }

  private func activitySymbol(_ kind: String) -> String {
    switch kind {
    case "account": return "person.crop.circle"
    case "gateway": return "network"
    case "proxy": return "bolt.circle"
    case "quota": return "gauge.with.dots.needle.67percent"
    default: return "arrow.left.arrow.right.circle"
    }
  }
}
