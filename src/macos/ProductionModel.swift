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
  private var serviceRequests = [String: (id: String, operation: DesignServiceOperation)]()
  private var clearedActivityIDs = Set<String>()
  private var clearedLogLineCount = [String: Int]()
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
      DispatchQueue.main.async { self?.synchronize() }
    }
    synchronize()
  }

  func consume(_ data: Data) {
    store.consume(data)
  }

  func persistAppearance() {
    TrayPreferences.appearance = appearance
  }

  override func refresh() {
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
    switchPhase = .inFlight(appID: appID, from: from, to: action.label)
    store.invoke(action)
  }

  override func updateAppConfiguration(
    appID: String,
    routeID: String,
    fastModel: String,
    reasoningModel: String,
    useCompatibleFallback: Bool
  ) {
    guard let app = apps.first(where: { $0.id == appID }) else { return }
    if routeID != app.selectedRouteID {
      setRoute(appID: appID, routeID: routeID)
      return
    }
    guard let config = store.clientModelConfigs.first(where: { $0.clientId == appID }),
      config.editable
    else {
      showFeedback("Model roles are managed by the selected app route.", kind: .information)
      return
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
      return
    }
    _ = store.applyModelRoles(clientId: appID, roleActionIds: roles, label: app.name)
  }

  override func updateAppModelRoles(
    appID: String,
    routeID: String,
    modelRoles: [String: String]
  ) {
    guard let app = apps.first(where: { $0.id == appID }) else { return }
    if routeID != app.selectedRouteID {
      setRoute(appID: appID, routeID: routeID)
      return
    }
    guard let config = store.clientModelConfigs.first(where: { $0.clientId == appID }),
      config.editable
    else {
      showFeedback("Model roles are managed by the selected app route.", kind: .information)
      return
    }
    var actionIDs = [String: String]()
    for (roleID, modelID) in modelRoles where !modelID.isEmpty {
      if let option = config.options.first(where: { $0.modelId == modelID }) {
        actionIDs[roleID] = option.actionId
      }
    }
    guard actionIDs["default"] != nil else {
      showFeedback("Choose a default model published by the current route.", kind: .error)
      return
    }
    _ = store.applyModelRoles(clientId: appID, roleActionIds: actionIDs, label: app.name)
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
    return store.mutate(
      operation: "account-save",
      name: name,
      providerId: source.providerId,
      sourceId: source.sourceId,
      label: label,
      activityLabel: "saving \(source.label) account"
    ) != nil
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
    let accepted =
      store.mutate(
        operation: operation,
        name: name,
        providerId: gateway.id,
        label: label,
        endpoint: endpoint,
        apiKey: apiKey,
        region: region,
        defaultModel: defaultModel,
        activityLabel: "adding \(gateway.label)"
      ) != nil
    if accepted {
      pendingGatewayConnectName = name
    }
    return accepted
  }

  override func updateAccount(id: String, label: String) {
    if let account = store.accounts.first(where: { $0.id == id }) {
      _ = store.mutate(
        operation: "account-edit",
        name: account.name,
        providerId: account.providerId,
        label: label,
        activityLabel: "updating \(account.label)"
      )
    } else if let gateway = gatewaySnapshot(for: id) {
      _ = store.mutate(
        operation: "gateway-edit",
        name: gateway.name,
        providerId: gateway.providerId,
        label: label,
        activityLabel: "updating \(gateway.name)"
      )
    }
  }

  override func refreshAccount(id: String) {
    if let account = store.accounts.first(where: { $0.id == id }) {
      _ = store.mutate(
        operation: "account-refresh",
        name: account.name,
        providerId: account.providerId,
        activityLabel: "refreshing \(account.label)"
      )
    } else if let gateway = gatewaySnapshot(for: id) {
      _ = store.mutate(
        operation: "gateway-refresh",
        name: gateway.name,
        providerId: gateway.providerId,
        activityLabel: "refreshing \(gateway.name) models"
      )
    }
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

  override func removeAccount(id: String) {
    if let account = store.accounts.first(where: { $0.id == id }) {
      _ = store.mutate(
        operation: "account-remove",
        name: account.name,
        providerId: account.providerId,
        activityLabel: "removing \(account.label)"
      )
    } else if let gateway = gatewaySnapshot(for: id) {
      _ = store.mutate(
        operation: "gateway-remove",
        name: gateway.name,
        providerId: gateway.providerId,
        activityLabel: "removing \(gateway.name)"
      )
    }
  }

  override func setHubSource(_ accountID: String, enabled: Bool) {
    guard let source = store.hubSources.first(where: { $0.id == accountID }) else { return }
    _ = store.mutate(
      operation: "hub-source-toggle",
      name: source.name,
      providerId: source.providerId,
      enabled: enabled,
      activityLabel: "updating Proxy Hub sources"
    )
  }

  override func selectRoutingCandidate(actionID: String) {
    _ = store.invoke(actionId: actionID, label: "select Proxy Hub source")
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
    serviceOperations[serviceID] = operation
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
    serviceOperations[serviceID] = .restarting
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
    else { return }
    let selected = entries.filter { accountIDs.contains($0.id) }
    let entryIds =
      overwrite
      ? selected.map(\.id)
      : selected.filter { $0.disposition != "replace" }.map(\.id)
    guard !entryIds.isEmpty else { return }
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

  override func deleteBackup(id: String) {
    store.clearBackupPreview()
    backups.removeAll { $0.id == id }
  }

  override func clearActivity() {
    if activityShowsLogs, let source = selectedLogSource,
      let text = store.logsBySource[source.id]
    {
      clearedLogLineCount[source.id] =
        text.split(
          separator: "\n",
          omittingEmptySubsequences: false
        ).count
    } else {
      clearedActivityIDs.formUnion(store.activity.map(\.id))
    }
    activity = designActivity()
    showFeedback("Cleared the visible activity list. Supervisor history is unchanged.")
  }

  override var hasInstalledApp: Bool { store.hasInstalledClient }

  override var hasChosenRoute: Bool {
    store.routes.contains { ($0.source?.isEmpty == false) }
      || store.actions.contains(where: \.selected)
  }

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
    guard store.settings.launchAtLogin != enabled else { return }
    _ = store.mutate(
      operation: "setting-launch-at-login",
      name: "settings",
      enabled: enabled,
      activityLabel: "updating login settings"
    )
  }

  override func setShowUsage(_ enabled: Bool) {
    guard store.settings.showQuota != enabled else { return }
    _ = store.mutate(
      operation: "setting-show-quota",
      name: "settings",
      enabled: enabled,
      activityLabel: "updating quota visibility"
    )
  }

  override func setStartProxiesAtLaunch(_ enabled: Bool) {
    guard store.settings.startEnabledProxies != enabled else { return }
    _ = store.mutate(
      operation: "setting-auto-start-proxies",
      name: "settings",
      enabled: enabled,
      activityLabel: "updating proxy auto-start"
    )
  }

  override func setWarnBeforeLowUsage(_ enabled: Bool) {
    guard store.settings.quotaGuardEnabled != enabled else { return }
    _ = store.mutate(
      operation: "setting-quota-guard",
      name: "settings",
      enabled: enabled,
      activityLabel: "updating Quota Guard"
    )
  }

  override func setActivityShowsLogs(_ enabled: Bool) {
    activityShowsLogs = enabled
    if enabled, let source = selectedLogSource {
      store.selectLogSource(source.id)
      store.requestLogs(source)
    }
    liveLogLines = designLogLines()
    activity = designActivity()
  }

  override func selectLogSource(_ id: String) {
    guard let source = store.logSources.first(where: { $0.id == id }) else { return }
    selectedLogSourceID = id
    store.selectLogSource(id)
    store.requestLogs(source)
    liveLogLines = designLogLines()
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
    if case .degraded = store.serviceState {
      phase = .error
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
    routingConflicts = store.hubConflicts.map(designRoutingConflict)
    publishedModels = designPublishedModels()
    proxyHubEnabled = store.proxyHub?.running == true
    proxyHubModelCount = store.proxyHub?.modelCount
    proxyHubPort = port(from: store.proxyHub?.address) ?? proxyHubPort
    logSources = store.logSources.map {
      DesignLogSource(
        id: $0.id,
        label: $0.label,
        detail: $0.detail,
        provider: $0.providerId,
        symbol: providerSymbol($0.providerId)
      )
    }
    selectedLogSourceID = selectedLogSource?.id ?? ""
    synchronizeLogState()
    openAtLogin = store.settings.launchAtLogin
    showUsage = store.settings.showQuota
    startProxiesAtLaunch = store.settings.startEnabledProxies
    warnBeforeLowUsage = store.settings.quotaGuardEnabled
    let wasBusy = isBusy
    isBusy = store.busyRequestId != nil
    if wasBusy, !isBusy {
      switchingAccountID = nil
      if !pendingRouteChanges.isEmpty {
        applyNextPendingRouteChange()
      }
    }
    if wasBusy, !isBusy, pendingRouteChanges.isEmpty,
      case .inFlight(let appID, let from, let to) = switchPhase,
      let result = store.lastResult
    {
      if result.status == "success" {
        switchPhase = .success(appID: appID, name: to)
        Task { @MainActor in
          try? await Task.sleep(nanoseconds: 1_600_000_000)
          if case .success = switchPhase { switchPhase = .idle }
        }
      } else {
        switchPhase = .recovery(appID: appID, requested: to, kept: from)
      }
    }
    liveLogLines = designLogLines()
    activity = designActivity()
    synchronizeOnboarding()
    synchronizeBackupPreview()
    synchronizeBackupInspectionState()

    if let result = store.lastResult, result.requestId != lastResultId {
      lastResultId = result.requestId
      if let service = serviceRequests.removeValue(forKey: result.requestId) {
        serviceOperations.removeValue(forKey: service.id)
        if result.status == "success" {
          serviceFailures.removeValue(forKey: service.id)
        } else {
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
    guard !actions.isEmpty || provider != nil else { return nil }
    let route = store.route(clientId: clientID)
    let selected = actions.first(where: \.selected)
    let config = store.modelConfig(clientID)
    let options = nativeFirstRoutes(
      actions.map {
        DesignRoute(
          id: $0.id,
          name: $0.routeKind == "direct-account"
            ? ($0.upstreamSourceLabel ?? $0.label)
            : $0.label,
          detail: $0.detail ?? $0.routeKind ?? "Available route",
          symbol: routeSymbol($0.routeKind),
          kind: designRouteKind($0.routeKind),
          providerID: $0.upstreamProviderId
        )
      })
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
          resetsAtMs: $0.resetsAtMs
        )
      } ?? []
    return DesignApp(
      id: clientID,
      name: designAppName(clientID),
      detail: connectionDetail,
      symbol: appSymbol(clientID),
      status: status,
      showInQuickPanel: AppPreferences.quickPanelShown(for: clientID) ?? true,
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
      name: proxy.label,
      detail: proxy.address ?? proxy.detail,
      symbol: providerSymbol(proxy.providerId),
      provider: proxy.providerId,
      status: proxy.running ? .running : proxy.enabled && required ? .attention : .stopped,
      port: port(from: proxy.address) ?? 0,
      enabled: proxy.running
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

  private func designActivity() -> [DesignActivity] {
    if activityShowsLogs, let source = selectedLogSource,
      let text = store.logsBySource[source.id], !text.isEmpty
    {
      let lines = text.split(separator: "\n", omittingEmptySubsequences: false)
      let hiddenCount = min(clearedLogLineCount[source.id] ?? 0, lines.count)
      return lines.dropFirst(hiddenCount).suffix(160).reversed()
        .enumerated().map { index, line in
          DesignActivity(
            id: "log-\(index)-\(line.hashValue)",
            time: "",
            title: source.label,
            detail: String(line),
            symbol: "terminal",
            status: line.localizedCaseInsensitiveContains("error") ? .attention : .ready
          )
        }
    }
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
        status: $0.isError ? .attention : .ready
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
    let lines = text.split(separator: "\n", omittingEmptySubsequences: false)
    let hiddenCount = min(clearedLogLineCount[source.id] ?? 0, lines.count)
    return lines.dropFirst(hiddenCount).suffix(160).map(String.init)
  }

  private func synchronizeLogState() {
    guard let source = selectedLogSource else {
      logState = .unavailable
      logStateMessage = "Choose another log source or return to Activity."
      return
    }
    if store.loadingLogSourceId == source.id {
      logState = .loading
      logStateMessage = "Loading \(source.label) logs…"
      return
    }
    let serviceRunning: Bool =
      if store.proxyHub?.id == source.id {
        store.proxyHub?.running == true
      } else if let proxy = store.proxies.first(where: { $0.id == source.id }) {
        proxy.running
      } else {
        true
      }
    if !serviceRunning {
      logState = .stopped
      logStateMessage = "Prior logs remain visible. Start the service to view new entries."
      return
    }
    switch store.logStatesBySource[source.id] {
    case "error":
      logState = .error
      logStateMessage = "Showing the most recent logs that loaded successfully. Try again."
    case "empty":
      logState = .empty
      logStateMessage = "The service is running but has not written any log entries."
    case "ready":
      logState = .ready
      logStateMessage = nil
    default:
      logState = store.logsBySource[source.id]?.isEmpty == false ? .ready : .idle
      logStateMessage = nil
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

  private func designRoutingConflict(_ conflict: HubConflictSnapshot) -> DesignRoutingConflict {
    let kind: DesignRoutingConflictKind =
      conflict.kind == "source-choice" ? .sourceChoice : .modelOverlap
    let models = conflict.models
    let title: String
    if let explicit = conflict.title, !explicit.isEmpty, kind == .sourceChoice {
      title = explicit.replacingOccurrences(of: "Choose one ", with: "")
        .replacingOccurrences(of: " account", with: "")
    } else if kind == .sourceChoice {
      title = "Account"
    } else if models.count == 1 {
      title = models[0]
    } else {
      title = "\(models.count) Models"
    }
    return DesignRoutingConflict(
      id: conflict.id,
      kind: kind,
      title: title,
      detail: DesignHubRouting.modelSummary(models),
      models: models,
      needsAttention: true,
      candidates: conflict.candidates.map {
        DesignRoutingCandidate(
          id: $0.id,
          label: $0.label,
          detail: $0.detail,
          provider: nativeProviderName($0.providerId),
          actionID: $0.actionId
        )
      }
    )
  }

  private func designPublishedModels() -> [DesignPublishedModel] {
    if !store.hubPublishedModels.isEmpty {
      return store.hubPublishedModels.map {
        DesignPublishedModel(
          modelID: $0.id,
          ownerAccountID: $0.ownerAccountId,
          ownerName: $0.ownerLabel,
          ownerProvider: nativeProviderName($0.ownerProviderId),
          needsChoice: $0.needsChoice
        )
      }
    }
    return store.hubConflicts.flatMap { conflict in
      conflict.models.map { model in
        DesignPublishedModel(
          modelID: model,
          ownerAccountID: nil,
          ownerName: "Needs a choice",
          ownerProvider: nativeProviderName(conflict.candidates.first?.providerId ?? ""),
          needsChoice: true
        )
      }
    }
  }

  override var savedAccountProviderOptions: [DesignProviderOption] {
    store.accountProviders.map {
      DesignProviderOption(
        id: $0.id,
        label: $0.label,
        detail: $0.detail,
        enabled: $0.installed || ($0.loginMethods ?? []).contains(where: \.available),
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
        providesEndpoint: $0.providesEndpoint == true,
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

  private func port(from address: String?) -> Int? {
    guard let address else { return nil }
    return URL(string: address)?.port
  }

  private func designServiceID(_ proxy: ProxySnapshot) -> String {
    proxy.providerId == "proxy-hub" ? "hub" : proxy.id
  }

  private func providerSymbol(_ provider: String) -> String {
    switch provider.lowercased() {
    case "claude", "anthropic": return "sparkles"
    case "codex", "openai", "opencode": return "terminal"
    case "gemini", "google", "antigravity": return "diamond.fill"
    case "kiro": return "shippingbox.fill"
    case "proxy-hub": return "point.3.connected.trianglepath.dotted"
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

  private func designRouteKind(_ kind: String?) -> DesignRouteKind {
    switch kind {
    case "hub": return .hub
    case "gateway": return .gateway
    case "direct-account": return .native
    case "account", "pool": return .account
    default: return .account
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
    let priority = ["claude", "codex", "gemini", "kiro", "grok", "opencode"]
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
