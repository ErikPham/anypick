import AppKit
import Foundation
import SwiftUI
import UniformTypeIdentifiers

@MainActor
class MacOSDesignModel: ObservableObject {
  @Published var destination: DesignDestination
  @Published var phase: DesignLoadPhase
  @Published var appearance: DesignAppearance
  @Published var activityShowsLogs: Bool
  @Published var apps: [DesignApp]
  @Published var accounts: [DesignAccount]
  @Published var proxies: [DesignProxy]
  @Published var activity: [DesignActivity]
  @Published var liveLogLines: [String]
  @Published var logSources: [DesignLogSource]
  @Published var selectedLogSourceID: String
  @Published var logState: DesignLogState = .idle
  @Published var logStateMessage: String?
  @Published var backups: [DesignBackup]
  @Published var backupInspectionState: DesignBackupInspectionState = .idle
  @Published var backupInspectionMessage: String?
  @Published var hubSourceIDs: Set<String>
  @Published var hubSources: [DesignHubSource]
  @Published var routingConflicts: [DesignRoutingConflict] = []
  @Published var publishedModels: [DesignPublishedModel] = []
  @Published var proxyHubModelCount: Int?
  @Published var proxyHubEnabled = true
  @Published var proxyHubPort = 41_401
  @Published var publishModels = true
  @Published var openAtLogin = true
  @Published var showUsage = true
  @Published var startProxiesAtLaunch = true
  @Published var warnBeforeLowUsage = true
  @Published var lowUsageThreshold = 10
  @Published var showGettingStarted = false
  @Published var showFirstRunWelcome = false
  @Published var onboardingDismissed = false
  @Published var useTrustedProfiles = true
  @Published var announcementRequested = 0
  @Published var feedback: DesignFeedback?
  @Published var pendingStartupFlow: String?
  @Published var pendingGatewayConnectID: String?
  @Published var pendingGatewayConnectName: String?
  @Published var liveLogins: [DesignLiveLogin] = []
  @Published var pendingImportBackupID: String?
  @Published var isBusy = false
  @Published var switchingAccountID: String?
  @Published var operationResult: DesignOperationResult?
  @Published var trayScene: DesignTrayScene = .auto
  @Published var switchPhase: DesignSwitchPhase = .idle
  @Published var trayUseWithAppID: String?
  @Published var serviceOperations: [String: DesignServiceOperation] = [:]
  @Published var serviceFailures: [String: String] = [:]
  @Published var degradedConditions: [DesignDegradedCondition] = []
  private var undoAction: DesignUndoAction?
  private var hubCatalogs: [DesignHubCatalog] = []
  private var hubModelOwners: [String: String] = [:]

  var isProduction: Bool { false }

  var currentLiveLogins: [DesignLiveLogin] { liveLogins }

  var currentLiveLogin: DesignLiveLogin? { currentLiveLogins.first }

  init(
    destination: DesignDestination,
    phase: DesignLoadPhase,
    appearance: DesignAppearance,
    activityShowsLogs: Bool,
    apps: [DesignApp],
    accounts: [DesignAccount],
    proxies: [DesignProxy],
    activity: [DesignActivity],
    liveLogLines: [String] = [],
    logSources: [DesignLogSource] = [],
    selectedLogSourceID: String = "",
    backups: [DesignBackup],
    startupFlow: String? = nil
  ) {
    self.destination = destination
    self.phase = phase
    self.appearance = appearance
    self.activityShowsLogs = activityShowsLogs
    self.apps = Self.applyingQuickPanelPreferences(apps)
    self.accounts = accounts
    self.proxies = proxies
    self.activity = activity
    self.liveLogLines = liveLogLines
    self.logSources = logSources
    self.selectedLogSourceID =
      selectedLogSourceID.isEmpty
      ? logSources.first?.id ?? ""
      : selectedLogSourceID
    self.backups = backups
    pendingStartupFlow = startupFlow
    hubCatalogs = DesignHubRouting.catalogs(from: accounts)
    let initialHubSourceIDs = DesignHubRouting.defaultEnabledIDs(in: accounts)
    hubSourceIDs = initialHubSourceIDs
    hubSources = DesignHubRouting.sources(from: accounts, enabledIDs: initialHubSourceIDs)
    proxyHubModelCount = nil
    let compiled = DesignHubRouting.compile(
      sources: hubSources,
      catalogs: hubCatalogs,
      owners: hubModelOwners
    )
    hubSources = compiled.sources
    publishedModels = compiled.models
    routingConflicts = compiled.conflicts
    proxyHubModelCount = compiled.models.filter { !$0.needsChoice }.count
  }

  var savedAccountProviderOptions: [DesignProviderOption] {
    [
      DesignProviderOption(
        id: "Claude",
        label: "Claude",
        detail: "Local app login",
        canClear: true,
        loginMethods: [
          DesignLoginMethod(
            id: "claude-subscription",
            label: "Claude subscription",
            detail: "Claude Code browser sign-in",
            provenance: "official",
            preferred: true,
            available: true
          )
        ]
      ),
      DesignProviderOption(
        id: "OpenAI",
        label: "OpenAI",
        detail: "Local app login",
        canClear: true,
        loginMethods: [
          DesignLoginMethod(
            id: "chatgpt-subscription",
            label: "ChatGPT subscription",
            detail: "OpenAI Codex browser sign-in",
            provenance: "official",
            preferred: true,
            available: true
          )
        ]
      ),
      DesignProviderOption(
        id: "Gemini", label: "Gemini", detail: "Local app login", canClear: true),
      DesignProviderOption(
        id: "Kiro",
        label: "Kiro",
        detail: "Local app login",
        canClear: true,
        loginMethods: [
          DesignLoginMethod(
            id: "builder-id",
            label: "Builder ID or social login",
            detail: "Kiro browser sign-in",
            provenance: "official",
            preferred: true,
            available: true
          )
        ]
      ),
      DesignProviderOption(
        id: "Grok",
        label: "Grok",
        detail: "Local app login",
        canClear: true,
        loginMethods: [
          DesignLoginMethod(
            id: "grok-subscription",
            label: "Grok subscription",
            detail: "Grok browser sign-in",
            provenance: "official",
            preferred: true,
            available: true
          )
        ]
      ),
      DesignProviderOption(
        id: "OpenCode",
        label: "OpenCode",
        detail: "Local app login",
        canClear: true,
        loginMethods: [
          DesignLoginMethod(
            id: "opencode-login",
            label: "OpenCode login",
            detail: "OpenCode browser sign-in",
            provenance: "official",
            preferred: true,
            available: true
          )
        ]
      ),
      DesignProviderOption(
        id: "WorkBuddy",
        label: "WorkBuddy",
        detail: "Local app login",
        canClear: true,
        // Two regions, one preferred: a token issued by one host is rejected
        // by the other, so the user picks the site they actually signed in on.
        // Community, not official — this flow is undocumented.
        loginMethods: [
          DesignLoginMethod(
            id: "workbuddy-international",
            label: "WorkBuddy international",
            detail: "Browser sign-in at www.codebuddy.ai",
            provenance: "community",
            preferred: true,
            available: true
          ),
          DesignLoginMethod(
            id: "codebuddy-china",
            label: "CodeBuddy China",
            detail: "Browser sign-in at copilot.tencent.com",
            provenance: "community",
            preferred: false,
            available: true
          ),
        ]
      ),
    ]
  }

  var gatewayProviderOptions: [DesignProviderOption] {
    [
      DesignProviderOption(
        id: "OpenRouter", label: "OpenRouter", detail: "Multi-model API gateway",
        defaultEndpoint: "https://openrouter.ai/api/v1", kind: "hosted"
      ),
      DesignProviderOption(
        id: "OpenAI", label: "OpenAI", detail: "OpenAI API",
        defaultEndpoint: "https://api.openai.com/v1", firstPartyClientIDs: ["codex"], kind: "hosted"
      ),
      DesignProviderOption(
        id: "Anthropic", label: "Anthropic", detail: "Anthropic Claude API",
        defaultEndpoint: "https://api.anthropic.com", firstPartyClientIDs: ["claude"],
        kind: "hosted"
      ),
      DesignProviderOption(
        id: "Grok", label: "xAI", detail: "xAI Grok API",
        defaultEndpoint: "https://api.x.ai/v1", kind: "hosted"
      ),
      DesignProviderOption(
        id: "Gemini", label: "Gemini", detail: "Google Gemini API",
        defaultEndpoint: "https://generativelanguage.googleapis.com/v1beta",
        firstPartyClientIDs: ["gemini"], kind: "hosted"
      ),
      DesignProviderOption(
        id: "LiteLLM", label: "LiteLLM", detail: "Self-hosted proxy",
        defaultEndpoint: "http://127.0.0.1:4000", kind: "custom"
      ),
      DesignProviderOption(
        id: "Local", label: "Local Gateway", detail: "Local OpenAI-compatible gateway",
        defaultEndpoint: "http://127.0.0.1:8080/v1", kind: "custom"
      ),
      DesignProviderOption(
        id: "Custom", label: "Custom", detail: "Custom endpoint", kind: "custom"),
    ]
  }

  func defaultGatewayEndpoint(for provider: String) -> String? {
    gatewayProviderOptions.first(where: { $0.id == provider })?.defaultEndpoint
  }

  func preferredGatewayProvider(for clientID: String?) -> DesignProviderOption? {
    guard let clientID else { return nil }
    return gatewayProviderOptions.first { option in
      option.firstPartyClientIDs.contains {
        $0.caseInsensitiveCompare(clientID) == .orderedSame
      }
    }
  }

  func usageWindows(for account: DesignAccount) -> [DesignUsageWindow] {
    guard isCurrentAccount(account), showUsage else { return [] }
    return apps.first { app in
      app.name.localizedCaseInsensitiveContains(account.provider)
        || account.provider.localizedCaseInsensitiveContains(app.id)
    }?.usageWindows ?? []
  }

  static func fromEnvironment(_ environment: [String: String] = ProcessInfo.processInfo.environment)
    -> MacOSDesignModel
  {
    let fixture =
      DesignLoadPhase(rawValue: environment["ANYPICK_MACOS_FIXTURE"] ?? "ready")
      ?? (environment["ANYPICK_MACOS_FIXTURE"] == "populated" ? .ready : .ready)
    let destination =
      DesignDestination(
        rawValue: environment["ANYPICK_MACOS_DESTINATION"] ?? DesignDestination.overview.rawValue
      ) ?? .overview
    let appearance =
      DesignAppearance(rawValue: environment["ANYPICK_MACOS_APPEARANCE"] ?? "system")
      ?? .system
    let showsLogs = environment["ANYPICK_MACOS_ACTIVITY_MODE"] == "logs"
    let populated = DesignFixture.populated()
    let model = MacOSDesignModel(
      destination: destination,
      phase: fixture,
      appearance: appearance,
      activityShowsLogs: showsLogs,
      apps: fixture == .empty ? [] : populated.apps,
      accounts: fixture == .empty ? [] : populated.accounts,
      proxies: fixture == .empty ? [] : populated.proxies,
      activity: fixture == .empty ? [] : populated.activity,
      liveLogLines: fixture == .empty ? [] : populated.liveLogLines,
      logSources: fixture == .empty
        ? []
        : [
          DesignLogSource(
            id: "hub",
            label: "Proxy Hub",
            detail: "Shared local endpoint",
            provider: "proxy-hub",
            symbol: "point.3.connected.trianglepath.dotted"
          ),
          DesignLogSource(
            id: "claude-proxy",
            label: "Claude Proxy",
            detail: "Claude-compatible local proxy",
            provider: "claude",
            symbol: "brain.head.profile"
          ),
        ],
      selectedLogSourceID: "hub",
      backups: fixture == .empty ? [] : populated.backups,
      startupFlow: environment["ANYPICK_MACOS_FLOW"]
    )
    if fixture == .empty {
      model.proxyHubEnabled = false
      model.showFirstRunWelcome = !(environment["ANYPICK_MACOS_FLOW"] ?? "").hasPrefix("tray")
    }
    model.logState = fixture == .empty ? .unavailable : (showsLogs ? .ready : .idle)
    if environment["ANYPICK_MACOS_FLOW"] == "getting-started" {
      model.showGettingStarted = true
    }
    if fixture != .empty {
      model.liveLogins = [
        DesignLiveLogin(
          providerID: "claude",
          sourceID: nil,
          providerName: "Claude",
          appName: "Claude Code",
          identity: "alex@studio.example",
          symbol: "brain.head.profile"
        )
      ]
    }
    model.applyTrayScene(from: environment)
    return model
  }

  func applyTrayScene(from environment: [String: String]) {
    let flow = environment["ANYPICK_MACOS_FLOW"] ?? ""
    if let explicit = DesignTrayScene(rawValue: environment["ANYPICK_MACOS_TRAY"] ?? "") {
      trayScene = explicit
    } else if flow == "tray-attention" {
      trayScene = .attention
    } else if flow == "tray-use-with" {
      trayScene = .useWith
      trayUseWithAppID = "codex"
    } else if flow == "tray-switching" {
      trayScene = .useWith
      trayUseWithAppID = "claude"
      switchPhase = .inFlight(appID: "claude", from: "Work", to: "Personal")
      isBusy = true
    } else if flow == "tray-recovery" {
      trayScene = .useWith
      trayUseWithAppID = "claude"
      switchPhase = .recovery(appID: "claude", requested: "Personal", kept: "Work")
    } else if flow == "tray-first-run" || phase == .empty {
      trayScene = .firstRun
    } else if flow.hasPrefix("tray") {
      trayScene = .ready
    } else {
      trayScene = isProduction ? .auto : .ready
    }
    if trayScene == .attention, let index = apps.firstIndex(where: { $0.id == "kiro" }) {
      apps[index].status = .attention
    }
  }

  func isSwitching(_ appID: String) -> Bool {
    if case .inFlight(let id, _, _) = switchPhase { return id == appID }
    return false
  }

  var hasSavedLogin: Bool { !accounts.isEmpty }

  var hasInstalledApp: Bool { !apps.isEmpty }

  var hasChosenRoute: Bool {
    apps.contains { app in
      !app.selectedRouteID.isEmpty && !app.selectedRouteID.hasPrefix("native-")
    }
  }

  var isHubReady: Bool {
    proxyHubEnabled && !hubSourceIDs.isEmpty && routingConflicts.isEmpty
  }

  var isOnboardingCoreComplete: Bool { hasSavedLogin && hasInstalledApp && hasChosenRoute }

  var isFirstRun: Bool { accounts.isEmpty }

  var shouldShowOnboarding: Bool {
    if showGettingStarted { return true }
    if onboardingDismissed { return false }
    if isFirstRun { return true }
    return !isOnboardingCoreComplete
  }

  var attentionCount: Int {
    apps.filter { $0.status == .attention || $0.status == .unavailable }.count
      + accounts.filter { $0.status == .attention || $0.status == .unavailable }.count
      + proxies.filter { $0.status == .attention || $0.status == .unavailable }.count
  }

  var healthyAccountCount: Int {
    accounts.filter { $0.status == .ready || $0.status == .running }.count
  }

  func refresh() {
    recordActivity(
      title: "AnyPick refreshed",
      detail: "Apps, accounts, and local services are up to date.",
      symbol: "arrow.clockwise",
      status: .ready
    )
    showFeedback("Everything is up to date.", kind: .success)
  }

  func detectSavedAccount(provider: String) -> Bool {
    liveLogin(for: provider) != nil
  }

  func clearLiveLogin(provider: String) {
    liveLogins.removeAll {
      $0.providerID.caseInsensitiveCompare(provider) == .orderedSame
        || $0.providerName.caseInsensitiveCompare(provider) == .orderedSame
    }
    showFeedback(
      "The live \(provider) login was backed up and cleared in this fixture.",
      kind: .warning
    )
  }

  func liveLogin(for provider: String) -> DesignLiveLogin? {
    currentLiveLogins.first {
      $0.providerID.caseInsensitiveCompare(provider) == .orderedSame
        || $0.providerName.caseInsensitiveCompare(provider) == .orderedSame
    }
  }

  func upsertLiveLogin(provider: String, identity: String, appName: String? = nil) {
    let symbol = symbol(for: provider)
    let login = DesignLiveLogin(
      providerID: provider.lowercased(),
      sourceID: nil,
      providerName: provider,
      appName: appName ?? anypickDisplayName(provider),
      identity: identity,
      symbol: symbol
    )
    if let index = liveLogins.firstIndex(where: {
      $0.providerName.caseInsensitiveCompare(provider) == .orderedSame
        || $0.providerID.caseInsensitiveCompare(provider) == .orderedSame
    }) {
      liveLogins[index] = login
    } else {
      liveLogins.append(login)
    }
  }

  func testGateway(provider: String, endpoint: String, apiKey: String) -> Bool {
    !apiKey.localizedCaseInsensitiveContains("invalid")
  }

  /// VoiceOver-only success cue — no modal. Use for copy / non-blocking status.
  func announceOnly(_ message: String) {
    announce(message)
  }

  func announce(_ message: String) {
    announcementRequested += 1
    NSAccessibility.post(
      element: NSApplication.shared,
      notification: .announcementRequested,
      userInfo: [.announcement: message, .priority: NSAccessibilityPriorityLevel.medium.rawValue]
    )
  }

  func applyRoute(appID: String, routeID: String) {
    guard let index = apps.firstIndex(where: { $0.id == appID }) else { return }
    let previousRoute = apps[index].selectedRouteID
    let previousStatus = apps[index].status
    guard previousRoute != routeID else { return }
    commitRoute(
      appID: appID,
      routeID: routeID,
      previousRoute: previousRoute,
      previousStatus: previousStatus
    )
  }

  var quickPanelApps: [DesignApp] { apps.filter(\.showInQuickPanel) }

  func setQuickPanelShown(appID: String, shows: Bool) {
    guard let index = apps.firstIndex(where: { $0.id == appID }) else { return }
    guard apps[index].showInQuickPanel != shows else { return }
    let previous = apps[index].showInQuickPanel
    apps[index].showInQuickPanel = shows
    AppPreferences.setQuickPanelShown(shows, for: appID)
    undoAction = .quickPanel(appID: appID, shows: previous)
    recordActivity(
      title: "\(apps[index].name) \(shows ? "added to" : "removed from") Quick Panel",
      detail: shows
        ? "It now appears in the Quick Panel." : "It no longer appears in the Quick Panel.",
      symbol: "menubar.rectangle",
      status: shows ? .ready : .stopped
    )
    showFeedback(
      "\(apps[index].name) \(shows ? "now shows in" : "no longer shows in") the Quick Panel.",
      offersUndo: true
    )
  }

  static func applyingQuickPanelPreferences(_ apps: [DesignApp]) -> [DesignApp] {
    apps.map { app in
      var copy = app
      if let shown = AppPreferences.quickPanelShown(for: app.id) {
        copy.showInQuickPanel = shown
      }
      return copy
    }
  }

  func setRoute(appID: String, routeID: String) {
    guard let index = apps.firstIndex(where: { $0.id == appID }) else { return }
    let previousRoute = apps[index].selectedRouteID
    let previousStatus = apps[index].status
    guard previousRoute != routeID else { return }
    let fromName = routeName(previousRoute, for: apps[index])
    let toName = routeName(routeID, for: apps[index])
    if isProduction {
      commitRoute(
        appID: appID, routeID: routeID, previousRoute: previousRoute, previousStatus: previousStatus
      )
      return
    }
    isBusy = true
    switchPhase = .inFlight(appID: appID, from: fromName, to: toName)
    Task { @MainActor in
      try? await Task.sleep(nanoseconds: 650_000_000)
      commitRoute(
        appID: appID, routeID: routeID, previousRoute: previousRoute, previousStatus: previousStatus
      )
      isBusy = false
      switchPhase = .success(appID: appID, name: toName)
      announce("\(apps[index].name) now uses \(toName)")
      try? await Task.sleep(nanoseconds: 1_600_000_000)
      if case .success = switchPhase {
        switchPhase = .idle
      }
    }
  }

  private func commitRoute(
    appID: String,
    routeID: String,
    previousRoute: String,
    previousStatus: DesignStatus
  ) {
    guard let index = apps.firstIndex(where: { $0.id == appID }) else { return }
    apps[index].selectedRouteID = routeID
    apps[index].status = .ready
    undoAction = .route(appID: appID, routeID: previousRoute, status: previousStatus)
    recordActivity(
      title: "\(apps[index].name) now uses \(routeName(routeID, for: apps[index]))",
      detail: "The change applied without restarting the app.",
      symbol: "arrow.triangle.2.circlepath",
      status: .ready
    )
    showFeedback(
      "\(apps[index].name) now uses \(routeName(routeID, for: apps[index])).",
      offersUndo: true
    )
  }

  func updateAppConfiguration(
    appID: String,
    routeID: String,
    fastModel: String,
    reasoningModel: String,
    useCompatibleFallback: Bool
  ) {
    guard let index = apps.firstIndex(where: { $0.id == appID }) else { return }
    let previous = apps[index]
    apps[index].selectedRouteID = routeID
    apps[index].fastModel = fastModel
    apps[index].reasoningModel = reasoningModel
    apps[index].useCompatibleFallback = useCompatibleFallback
    apps[index].status = routeID.hasPrefix("native-") ? .attention : .ready
    undoAction = .appConfiguration(previous)
    recordActivity(
      title: "\(apps[index].name) settings applied",
      detail: "Account choice and model roles were updated together.",
      symbol: "slider.horizontal.3",
      status: apps[index].status
    )
    showFeedback("Applied settings for \(apps[index].name).", kind: .success, offersUndo: true)
  }

  func updateAppModelRoles(appID: String, routeID: String, modelRoles: [String: String]) {
    guard let index = apps.firstIndex(where: { $0.id == appID }) else { return }
    apps[index].selectedRouteID = routeID
    for roleIndex in apps[index].modelRoles.indices {
      let roleID = apps[index].modelRoles[roleIndex].id
      apps[index].modelRoles[roleIndex].modelID = modelRoles[roleID] ?? ""
    }
    showFeedback("Applied model roles for \(apps[index].name).", kind: .success)
  }

  func resetAppConfiguration(appID: String) {
    guard let index = apps.firstIndex(where: { $0.id == appID }) else { return }
    let previous = apps[index]
    apps[index].selectedRouteID = "native-\(appID)"
    apps[index].fastModel = ""
    apps[index].reasoningModel = ""
    apps[index].useCompatibleFallback = true
    apps[index].status = .attention
    undoAction = .appConfiguration(previous)
    recordActivity(
      title: "\(apps[index].name) reset",
      detail: "AnyPick-managed account and model overrides were removed.",
      symbol: "arrow.counterclockwise.circle",
      status: .attention
    )
    showFeedback("Reset \(apps[index].name) to its native login.", kind: .warning, offersUndo: true)
  }

  func addSavedAccount(
    provider: String,
    name: String,
    label: String,
    identity: String
  ) -> Bool {
    let normalizedName = name.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !normalizedName.isEmpty else { return false }
    guard
      !accounts.contains(where: {
        $0.provider.caseInsensitiveCompare(provider) == .orderedSame
          && $0.name.caseInsensitiveCompare(normalizedName) == .orderedSame
      })
    else {
      showFeedback(
        "An account named \(normalizedName) already exists for \(provider).", kind: .error)
      return false
    }
    let account = DesignAccount(
      id: "\(provider.lowercased())-\(normalizedName.lowercased())-\(UUID().uuidString.prefix(6))",
      name: label.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? normalizedName : label,
      detail: identity,
      provider: provider,
      symbol: symbol(for: provider),
      kind: .saved,
      status: .ready,
      lastUsed: "Saved now"
    )
    accounts.append(account)
    if DesignHubRouting.isEligible(account) {
      hubSourceIDs.insert(account.id)
    }
    rebuildHubSources()
    phase = .ready
    recordActivity(
      title: "\(account.name) saved",
      detail: "\(provider) login is now available to supported apps.",
      symbol: "person.crop.circle.badge.checkmark",
      status: .ready
    )
    showFeedback("Saved \(account.name).", kind: .success)
    return true
  }

  func saveCurrentLogin(_ login: DesignLiveLogin) -> Bool {
    guard
      addSavedAccount(
        provider: login.providerName,
        name: "Work",
        label: "",
        identity: login.identity ?? "Current login"
      )
    else { return false }
    trayScene = .ready
    return true
  }

  func loginSavedAccount(
    provider: String,
    name: String,
    label: String,
    methodId: String
  ) -> Bool {
    let identity = liveLogin(for: provider)?.identity ?? "Signed in with \(methodId)"
    let requestedName = name.trimmingCharacters(in: .whitespacesAndNewlines)
    let fallbackName = "subscription-\(accounts.filter { $0.provider == provider }.count + 1)"
    return addSavedAccount(
      provider: provider,
      name: requestedName.isEmpty ? fallbackName : requestedName,
      label: label,
      identity: identity
    )
  }

  func cancelAccountLogin() {}

  func addGateway(
    provider: String,
    name: String,
    label: String,
    endpoint: String,
    apiKey: String,
    region: String?,
    defaultModel: String
  ) -> Bool {
    let normalizedName = name.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !normalizedName.isEmpty else { return false }
    guard
      !accounts.contains(where: { $0.name.caseInsensitiveCompare(normalizedName) == .orderedSame })
    else {
      showFeedback("A connection named \(normalizedName) already exists.", kind: .error)
      return false
    }
    let detail = [endpoint.trimmingCharacters(in: .whitespacesAndNewlines), defaultModel]
      .filter { !$0.isEmpty }
      .joined(separator: " · ")
    let gateway = DesignAccount(
      id: "gateway-\(normalizedName.lowercased())-\(UUID().uuidString.prefix(6))",
      name: label.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? normalizedName : label,
      detail: detail.isEmpty ? "Default endpoint" : detail,
      provider: provider,
      symbol: symbol(for: provider),
      kind: .gateway,
      status: .ready,
      lastUsed: "Created now"
    )
    accounts.append(gateway)
    recordActivity(
      title: "\(gateway.name) gateway added",
      detail: "\(provider) is ready as an app route.",
      symbol: "network.badge.shield.half.filled",
      status: .ready
    )
    showFeedback("Added \(gateway.name).", kind: .success)
    pendingGatewayConnectID = gateway.id
    return true
  }

  func updateAccount(id: String, label: String) {
    guard let index = accounts.firstIndex(where: { $0.id == id }) else { return }
    let cleaned = label.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !cleaned.isEmpty else { return }
    accounts[index].name = cleaned
    accounts[index].lastUsed = "Updated now"
    recordActivity(
      title: "\(cleaned) updated",
      detail: "The connection label was changed.",
      symbol: "pencil.circle",
      status: .ready
    )
    showFeedback("Updated \(cleaned).", kind: .success)
  }

  func refreshAccount(id: String) {
    guard let index = accounts.firstIndex(where: { $0.id == id }) else { return }
    accounts[index].status = .ready
    accounts[index].lastUsed = "Refreshed now"
    recordActivity(
      title: "\(accounts[index].name) refreshed",
      detail: "The saved credential and model availability were checked.",
      symbol: "arrow.clockwise.circle",
      status: .ready
    )
    showFeedback("\(accounts[index].name) is ready.", kind: .success)
  }

  func isCurrentAccount(_ account: DesignAccount) -> Bool {
    account.kind == .saved && account.status == .running
  }

  func gatewayRoute(in app: DesignApp, matching gateway: DesignAccount) -> DesignRoute? {
    let rawID =
      gateway.id.hasPrefix("gateway/")
      ? String(gateway.id.dropFirst("gateway/".count))
      : gateway.id
    return app.routeOptions.first { route in
      guard route.kind == .gateway else { return false }
      if route.id == gateway.id || route.id == rawID { return true }
      if route.name.caseInsensitiveCompare(gateway.name) == .orderedSame { return true }
      if gateway.name.localizedCaseInsensitiveContains(route.name)
        || route.name.localizedCaseInsensitiveContains(gateway.name)
      {
        return true
      }
      guard let providerID = route.providerID else { return false }
      return providerID.caseInsensitiveCompare(gateway.provider) == .orderedSame
        || providerID.caseInsensitiveCompare(rawID) == .orderedSame
    }
  }

  func appsCompatible(with gateway: DesignAccount) -> [DesignApp] {
    apps.filter { gatewayRoute(in: $0, matching: gateway) != nil }
  }

  func gatewayRouteChanges(id: String, appIDs: Set<String>) -> [(appID: String, routeID: String)] {
    guard let gateway = accounts.first(where: { $0.id == id && $0.kind == .gateway }) else {
      return []
    }
    var changes = [(appID: String, routeID: String)]()
    for app in appsCompatible(with: gateway) {
      guard let route = gatewayRoute(in: app, matching: gateway) else { continue }
      let shouldUse = appIDs.contains(app.id)
      let using = app.selectedRouteID == route.id
      if shouldUse && !using {
        changes.append((app.id, route.id))
      } else if !shouldUse && using,
        let native = app.routeOptions.first(where: { $0.kind == .native })
      {
        changes.append((app.id, native.id))
      }
    }
    return changes
  }

  func connectGateway(id: String, appIDs: Set<String>) {
    guard let gateway = accounts.first(where: { $0.id == id && $0.kind == .gateway }) else {
      return
    }
    let changes = gatewayRouteChanges(id: id, appIDs: appIDs)
    if changes.isEmpty {
      showFeedback("No app routes needed to change.", kind: .information)
      return
    }
    for change in changes {
      applyRoute(appID: change.appID, routeID: change.routeID)
    }
    let names = apps.filter { appIDs.contains($0.id) }.map(\.name)
    let detail =
      names.isEmpty
      ? "No apps use \(gateway.name)."
      : "\(names.joined(separator: ", ")) now use \(gateway.name)."
    recordActivity(
      title: "\(gateway.name) connected",
      detail: detail,
      symbol: "network",
      status: .ready
    )
    showFeedback(detail, kind: .success)
  }

  func makeAccountCurrent(id: String) {
    guard let account = accounts.first(where: { $0.id == id }) else { return }
    guard !isCurrentAccount(account) else { return }
    if isProduction {
      applyMakeCurrent(id: id)
      return
    }
    isBusy = true
    switchingAccountID = id
    Task { @MainActor in
      try? await Task.sleep(nanoseconds: 650_000_000)
      applyMakeCurrent(id: id)
      isBusy = false
      switchingAccountID = nil
    }
  }

  func applyMakeCurrent(id: String) {
    guard let index = accounts.firstIndex(where: { $0.id == id }) else { return }
    let provider = accounts[index].provider
    for candidate in accounts.indices where accounts[candidate].provider == provider {
      accounts[candidate].status = candidate == index ? .running : .ready
      accounts[candidate].lastUsed = candidate == index ? "Used now" : "Saved"
    }
    recordActivity(
      title: "\(accounts[index].name) is current",
      detail: "The live \(provider) login now uses this saved account.",
      symbol: "person.crop.circle.badge.checkmark",
      status: .ready
    )
    showFeedback("\(accounts[index].name) is now current.", kind: .success)
    announce("\(accounts[index].name) is now current")
  }

  func removeAccount(id: String) {
    guard let index = accounts.firstIndex(where: { $0.id == id }) else { return }
    let removed = accounts.remove(at: index)
    hubSourceIDs.remove(id)
    rebuildHubSources()
    undoAction = .removedAccount(removed, index)
    recordActivity(
      title: "\(removed.name) removed",
      detail: "The saved \(removed.provider) connection was removed from AnyPick.",
      symbol: "trash",
      status: .attention
    )
    showFeedback("Removed \(removed.name).", kind: .warning, offersUndo: true)
  }

  func setHubSource(_ accountID: String, enabled: Bool) {
    if enabled { hubSourceIDs.insert(accountID) } else { hubSourceIDs.remove(accountID) }
    if let index = hubSources.firstIndex(where: { $0.id == accountID }) {
      hubSources[index].enabled = enabled
    }
    if hubSourceIDs.isEmpty { proxyHubEnabled = false }
    if enabled { proxyHubEnabled = true }
    recompileHubRouting()
    let name =
      hubSources.first(where: { $0.id == accountID })?.name
      ?? accounts.first(where: { $0.id == accountID })?.name
    guard let name else { return }
    recordActivity(
      title: "Proxy Hub sources updated",
      detail: "\(name) was \(enabled ? "included in" : "removed from") Proxy Hub.",
      symbol: "point.3.connected.trianglepath.dotted",
      status: enabled ? .ready : .attention
    )
  }

  func selectRoutingCandidate(actionID: String) {
    guard
      let conflict = routingConflicts.first(where: {
        $0.candidates.contains(where: { $0.actionID == actionID })
      }),
      let candidate = conflict.candidates.first(where: { $0.actionID == actionID })
    else { return }
    for model in conflict.models {
      hubModelOwners[model] = candidate.id
    }
    recompileHubRouting()
  }

  func recompileHubRouting() {
    let compiled = DesignHubRouting.compile(
      sources: hubSources,
      catalogs: hubCatalogs,
      owners: hubModelOwners
    )
    hubSources = compiled.sources
    publishedModels = compiled.models
    routingConflicts = compiled.conflicts
    proxyHubModelCount = compiled.models.filter { !$0.needsChoice }.count
  }

  private func rebuildHubSources() {
    hubCatalogs = DesignHubRouting.catalogs(from: accounts)
    hubSourceIDs = hubSourceIDs.intersection(Set(accounts.map(\.id)))
    hubSources = DesignHubRouting.sources(from: accounts, enabledIDs: hubSourceIDs)
    recompileHubRouting()
  }

  func setProxyEnabled(id: String, enabled: Bool) {
    guard let index = proxies.firstIndex(where: { $0.id == id }) else { return }
    let previousEnabled = proxies[index].enabled
    let previousStatus = proxies[index].status
    guard previousEnabled != enabled else { return }
    proxies[index].enabled = enabled
    proxies[index].status = enabled ? .running : .stopped
    if id == "hub" { proxyHubEnabled = enabled }
    undoAction = .proxy(id: id, enabled: previousEnabled, status: previousStatus)
    recordActivity(
      title: "\(proxies[index].name) \(enabled ? "started" : "stopped")",
      detail: "Local service on port \(proxies[index].port).",
      symbol: enabled ? "play.circle.fill" : "stop.circle",
      status: enabled ? .running : .stopped
    )
    showFeedback("\(proxies[index].name) \(enabled ? "started" : "stopped").", offersUndo: true)
  }

  func setProxyHubEnabled(_ enabled: Bool) {
    proxyHubEnabled = enabled
    recordActivity(
      title: "Proxy Hub \(enabled ? "started" : "stopped")",
      detail: enabled ? "Listening on localhost:\(proxyHubPort)." : "Proxy Hub is stopped.",
      symbol: enabled ? "play.circle.fill" : "stop.circle",
      status: enabled ? .running : .stopped
    )
    showFeedback("Proxy Hub \(enabled ? "started" : "stopped").")
  }

  func restartProxy(id: String) {
    if id == "hub" {
      proxyHubEnabled = true
      recordActivity(
        title: "Proxy Hub restarted",
        detail: "Listening on localhost:\(proxyHubPort).",
        symbol: "arrow.clockwise.circle.fill",
        status: .running
      )
      showFeedback("Restarted Proxy Hub.", kind: .success)
      return
    }
    guard let index = proxies.firstIndex(where: { $0.id == id }) else { return }
    proxies[index].enabled = true
    proxies[index].status = .running
    recordActivity(
      title: "\(proxies[index].name) restarted",
      detail: "Listening on localhost:\(proxies[index].port).",
      symbol: "arrow.clockwise.circle.fill",
      status: .running
    )
    showFeedback("Restarted \(proxies[index].name).", kind: .success)
  }

  func restartRunningProxies() {
    let runningIDs = proxies.filter(\.enabled).map(\.id)
    for id in runningIDs { restartProxy(id: id) }
    if runningIDs.isEmpty {
      showFeedback("There are no running proxies to restart.", kind: .information)
    } else {
      showFeedback(
        "Restarted \(runningIDs.count) running \(runningIDs.count == 1 ? "proxy" : "proxies").",
        kind: .success)
    }
  }

  func stopAllProxies() {
    for index in proxies.indices {
      proxies[index].enabled = false
      proxies[index].status = .stopped
    }
    recordActivity(
      title: "All local proxies stopped",
      detail: "Apps using direct native logins were not changed.",
      symbol: "stop.circle",
      status: .stopped
    )
    showFeedback("Stopped all local proxies.", kind: .warning)
  }

  @discardableResult
  func createBackup(accountIDs: Set<String>) -> Bool {
    let selected = accounts.filter { accountIDs.contains($0.id) && $0.kind == .saved }
    guard !selected.isEmpty, selected.count <= 64 else {
      showFeedback("Select between 1 and 64 saved accounts.", kind: .error)
      return false
    }
    let formatter = DateFormatter()
    formatter.dateFormat = "MMM d 'at' HH:mm"
    let backup = DesignBackup(
      id: "backup-\(UUID().uuidString)",
      createdAt: formatter.string(from: Date()),
      accountCount: selected.count,
      size: "\(max(8, selected.count * 7)) KB",
      location: "AnyPick Backups",
      accounts: selected
    )
    backups.insert(backup, at: 0)
    recordActivity(
      title: "Account backup created",
      detail: "\(selected.count) saved \(selected.count == 1 ? "account" : "accounts") included.",
      symbol: "externaldrive.badge.checkmark",
      status: .ready
    )
    showFeedback(
      "Created a backup with \(selected.count) \(selected.count == 1 ? "account" : "accounts").",
      kind: .success)
    return true
  }

  @discardableResult
  func importBackupPackage() -> Bool {
    if pendingStartupFlow == "import-backup" {
      pendingStartupFlow = nil
      return presentImportedBackup(fileName: "AnyPick Accounts.anypick-backup")
    }
    let panel = NSOpenPanel()
    panel.canChooseFiles = true
    panel.canChooseDirectories = false
    panel.allowsMultipleSelection = false
    panel.allowedContentTypes = [UTType(filenameExtension: "anypick-backup") ?? .data]
    panel.message = "Choose an AnyPick account backup."
    panel.prompt = "Choose"
    guard panel.runModal() == .OK, let url = panel.url else { return false }
    return presentImportedBackup(fileName: url.lastPathComponent)
  }

  @discardableResult
  func presentImportedBackup(fileName: String) -> Bool {
    guard var package = backups.first(where: { $0.id == "backup-import" }) ?? backups.last else {
      showFeedback("That file isn’t an AnyPick backup.", kind: .error)
      return false
    }
    package.location = fileName
    if let index = backups.firstIndex(where: { $0.id == package.id }) {
      backups[index] = package
    }
    pendingImportBackupID = package.id
    return true
  }

  func setOpenAtLogin(_ enabled: Bool) {
    showFeedback(enabled ? "AnyPick will open at login." : "Open at Login disabled.")
  }

  func setShowUsage(_ enabled: Bool) {
    showFeedback(enabled ? "Usage remaining is visible." : "Usage remaining is hidden.")
  }

  func setStartProxiesAtLaunch(_ enabled: Bool) {
    startProxiesAtLaunch = enabled
    showFeedback(
      enabled
        ? "Enabled local proxies will start at launch."
        : "Local proxies will only start when an app needs one."
    )
  }

  func setWarnBeforeLowUsage(_ enabled: Bool) {
    warnBeforeLowUsage = enabled
    showFeedback(enabled ? "Low-usage warnings are on." : "Low-usage warnings are off.")
  }

  func dismissOnboarding() {
    onboardingDismissed = true
    showGettingStarted = false
  }

  func dismissFirstRunWelcome() {
    showFirstRunWelcome = false
    showGettingStarted = true
    destination = .overview
  }

  func showGettingStartedAgain() {
    onboardingDismissed = false
    showGettingStarted = true
    destination = .overview
  }

  func setActivityShowsLogs(_ enabled: Bool) {
    activityShowsLogs = enabled
    if enabled, logState == .idle {
      logState = logSources.isEmpty ? .unavailable : (liveLogLines.isEmpty ? .empty : .ready)
    }
  }

  func selectLogSource(_ id: String) {
    guard logSources.contains(where: { $0.id == id }) else { return }
    selectedLogSourceID = id
    logState = liveLogLines.isEmpty ? .empty : .ready
    logStateMessage = nil
  }

  func refreshSelectedLogSource() {
    guard !selectedLogSourceID.isEmpty else { return }
    selectLogSource(selectedLogSourceID)
  }

  func setLogPolling(_ enabled: Bool) {}

  func restoreBackup(id: String, accountIDs: Set<String>, overwrite: Bool) {
    guard let backup = backups.first(where: { $0.id == id }) else { return }
    let selected = backup.accounts.filter { accountIDs.contains($0.id) }
    var restored = 0
    for account in selected {
      if let index = accounts.firstIndex(where: {
        $0.provider == account.provider && $0.name == account.name
      }) {
        if !overwrite { continue }
        accounts[index] = account
      } else {
        accounts.append(account)
      }
      if DesignHubRouting.isEligible(account) {
        hubSourceIDs.insert(account.id)
      }
      restored += 1
    }
    rebuildHubSources()
    backupInspectionState = .success
    backupInspectionMessage = "\(restored) \(restored == 1 ? "Account" : "Accounts") Imported"
    recordActivity(
      title: "Account backup restored",
      detail: "\(restored) saved \(restored == 1 ? "account" : "accounts") imported.",
      symbol: "arrow.down.doc.fill",
      status: .ready
    )
    showFeedback("Imported \(restored) \(restored == 1 ? "account" : "accounts").", kind: .success)
  }

  func deleteBackup(id: String) {
    guard let index = backups.firstIndex(where: { $0.id == id }) else { return }
    let removed = backups.remove(at: index)
    undoAction = .removedBackup(removed, index)
    showFeedback("Deleted backup from \(removed.createdAt).", kind: .warning, offersUndo: true)
  }

  func clearActivity() {
    activity.removeAll()
    liveLogLines.removeAll()
    showFeedback("Activity history cleared.", kind: .information)
  }

  func performUndo() {
    guard let action = undoAction else { return }
    switch action {
    case .removedAccount(let account, let index):
      accounts.insert(account, at: min(index, accounts.count))
      if DesignHubRouting.isEligible(account) { hubSourceIDs.insert(account.id) }
      rebuildHubSources()
    case .removedBackup(let backup, let index):
      backups.insert(backup, at: min(index, backups.count))
    case .route(let appID, let routeID, let status):
      guard let index = apps.firstIndex(where: { $0.id == appID }) else { break }
      apps[index].selectedRouteID = routeID
      apps[index].status = status
    case .appConfiguration(let app):
      guard let index = apps.firstIndex(where: { $0.id == app.id }) else { break }
      apps[index] = app
    case .proxy(let id, let enabled, let status):
      guard let index = proxies.firstIndex(where: { $0.id == id }) else { break }
      proxies[index].enabled = enabled
      proxies[index].status = status
      if id == "hub" { proxyHubEnabled = enabled }
    case .quickPanel(let appID, let shows):
      guard let index = apps.firstIndex(where: { $0.id == appID }) else { break }
      apps[index].showInQuickPanel = shows
      AppPreferences.setQuickPanelShown(shows, for: appID)
    }
    undoAction = nil
    showFeedback("Change undone.", kind: .information)
  }

  var canUndo: Bool { undoAction != nil }

  func dismissFeedback() {
    feedback = nil
  }

  /// Blocking alert only for errors. Success / info / undo stay silent
  /// (VoiceOver + Edit → Undo), matching System Settings.
  func showFeedback(
    _ message: String,
    kind: DesignFeedback.Kind = .information,
    offersUndo: Bool = false
  ) {
    announce(message)
    if !offersUndo { undoAction = nil }
    if kind == .error {
      feedback = DesignFeedback(message: message, kind: kind, offersUndo: false)
    } else {
      // Clear any previous error alert; do not present success modals.
      if feedback?.kind == .error { feedback = nil }
    }
  }

  private func recordActivity(
    title: String,
    detail: String,
    symbol: String,
    status: DesignStatus
  ) {
    let formatter = DateFormatter()
    formatter.dateFormat = "HH:mm"
    activity.insert(
      DesignActivity(
        id: UUID().uuidString,
        time: formatter.string(from: Date()),
        title: title,
        detail: detail,
        symbol: symbol,
        status: status
      ),
      at: 0
    )
  }

  private func routeName(_ routeID: String, for app: DesignApp) -> String {
    app.routeOptions.first(where: { $0.id == routeID })?.name ?? "Native Login"
  }

  func providerID(forRoute route: DesignRoute) -> String {
    if let providerID = route.providerID { return providerID }
    if route.kind == .gateway { return "openrouter" }
    if let account = accounts.first(where: { $0.id == route.id || $0.name == route.name }) {
      return account.provider
    }
    return route.id
  }

  /// SF Symbol fallback when a provider has no bundled brand mark.
  private func symbol(for provider: String) -> String {
    DesignBrandAsset.fallbackSymbol(for: provider)
  }

}
