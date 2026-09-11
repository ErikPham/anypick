import AppKit
import SwiftUI
import XCTest

final class AnyPickTests: XCTestCase {
  func testBuiltAppContainsExecutableGoHelper() throws {
    let products = Bundle(for: Self.self).bundleURL.deletingLastPathComponent()
    let helper = products
      .appendingPathComponent("AnyPick.app/Contents/Helpers/anypick-core").path

    XCTAssertTrue(FileManager.default.fileExists(atPath: helper))
    XCTAssertTrue(FileManager.default.isExecutableFile(atPath: helper))
  }

  func testBuiltAppContainsNoNodePluginHost() throws {
    let app = Bundle(for: Self.self).bundleURL
      .deletingLastPathComponent()
      .appendingPathComponent("AnyPick.app")
    let resources = app.appendingPathComponent("Contents/Resources")
    let fileManager = FileManager.default

    XCTAssertFalse(
      fileManager.fileExists(atPath: resources.appendingPathComponent("plugin-host").path),
      "ADR 0020 forbids plugin-host resources in the app bundle"
    )
    XCTAssertFalse(
      fileManager.fileExists(
        atPath: resources.appendingPathComponent("plugin-host/plugin-host.mjs").path
      )
    )
    XCTAssertFalse(
      fileManager.fileExists(
        atPath: resources.appendingPathComponent("plugins/popular-providers/index.mjs").path
      )
    )
  }

  func testProjectUsesTheSharedSwiftSourceManifest() throws {
    let macOSDirectory = URL(fileURLWithPath: #filePath)
      .deletingLastPathComponent()
      .deletingLastPathComponent()
    let manifest = try String(
      contentsOf: macOSDirectory.appendingPathComponent("sources.txt"),
      encoding: .utf8
    )
    let entries = manifest.split(whereSeparator: \.isNewline).map(String.init)
    let project = try String(
      contentsOf: macOSDirectory
        .appendingPathComponent("AnyPick.xcodeproj/project.pbxproj"),
      encoding: .utf8
    )

    XCTAssertTrue(entries.contains("AppEntry.swift"))
    XCTAssertTrue(entries.contains("ProductionModel.swift"))
    XCTAssertEqual(entries.count, Set(entries).count)
    for entry in entries {
      XCTAssertTrue(
        project.contains("path = \(entry);"),
        "AnyPick.xcodeproj does not reference shared source \(entry)"
      )
    }
  }

  func testAppServiceClientNegotiatesBeforeDeliveringFrames() async throws {
    let client = AppServiceClient(appVersion: "(devel)", launchAtLogin: false)
    let hello = try frame(
      "hello",
      [
        "protocolVersion": 2,
        "messageVersion": 1,
        "engineVersion": "(devel)",
        "schemaVersion": 4,
        "capabilities": ["command-dto-v1", "snapshot-dto-v1"],
        "instanceId": "0123456789abcdef0123456789abcdef",
        "maxFrameBytes": 4 * 1024 * 1024,
      ]
    )
    let helloActions = try await client.consume(Data("\(hello)\n".utf8))
    XCTAssertEqual(helloActions.count, 2)
    guard case .state(.handshaking) = helloActions[0],
          case .send(let hostHello) = helloActions[1] else {
      return XCTFail("client did not acknowledge engine hello")
    }
    XCTAssertTrue(hostHello.hasPrefix("hello\t"))

    let ready = try frame(
      "ready",
      [
        "protocolVersion": 2,
        "capabilities": ["command-dto-v1", "snapshot-dto-v1"],
      ]
    )
    let readyActions = try await client.consume(Data("\(ready)\n".utf8))
    guard readyActions.count == 1, case .state(.ready) = readyActions[0] else {
      return XCTFail("client did not enter ready state")
    }

    let snapshot = try frame("snapshot", ["revision": 1])
    let frameActions = try await client.consume(Data("\(snapshot)\n".utf8))
    guard frameActions.count == 1, case .deliver(let delivered) = frameActions[0] else {
      return XCTFail("ready client did not deliver snapshot")
    }
    XCTAssertEqual(String(decoding: delivered, as: UTF8.self), "\(snapshot)\n")
  }

  func testAppServiceClientRejectsProtocolMismatch() async throws {
    let client = AppServiceClient(appVersion: "1.0.0", launchAtLogin: false)
    let hello = try frame(
      "hello",
      [
        "protocolVersion": 99,
        "messageVersion": 1,
        "engineVersion": "1.0.0",
        "schemaVersion": 4,
        "capabilities": ["command-dto-v1", "snapshot-dto-v1"],
        "instanceId": "0123456789abcdef0123456789abcdef",
        "maxFrameBytes": 4 * 1024 * 1024,
      ]
    )
    do {
      _ = try await client.consume(Data("\(hello)\n".utf8))
      XCTFail("protocol mismatch was accepted")
    } catch let error as AppServiceProtocolError {
      XCTAssertTrue(error.localizedDescription.contains("protocol 99"))
    }
  }

  func testTraySnapshotDecodesPluginDegradedConditions() throws {
    let payload: [String: Any] = [
      "revision": 1,
      "proxyCount": 0,
      "routes": [],
      "actions": [],
      "usage": [],
      "proxies": [],
      "accounts": [],
      "gateways": [],
      "accountProviders": [],
      "gatewayProviders": [],
      "settings": [
        "launchAtLogin": false,
        "startEnabledProxies": true,
        "showQuota": true,
        "quotaGuardEnabled": false,
      ],
      "activity": [],
      "degradedConditions": [
        [
          "id": "plugin-host",
          "kind": "plugin-host",
          "title": "Plugin host unavailable",
          "detail": "The private plugin host stopped.",
        ]
      ],
    ]
    let data = try JSONSerialization.data(withJSONObject: payload)
    let snapshot = try JSONDecoder().decode(TraySnapshot.self, from: data)

    XCTAssertEqual(snapshot.degradedConditions?.first?.id, "plugin-host")
    XCTAssertEqual(snapshot.degradedConditions?.first?.kind, "plugin-host")
  }

  private func frame(_ prefix: String, _ object: Any) throws -> String {
    let data = try JSONSerialization.data(withJSONObject: object, options: [.sortedKeys])
    return "\(prefix)\t\(data.base64EncodedString())"
  }

  // MARK: - P1-01 route dedupe

  func testCompatibleRoutesDedupeKeepsCurrentOnce() {
    let selected = DesignRoute(
      id: "b", name: "Work", detail: "alex@studio.example", symbol: "person",
      kind: .account, providerID: "claude")
    let app = DesignApp(
      id: "claude", name: "Claude Code", detail: "Uses Work", symbol: "s",
      status: .ready, accountCount: 3, selectedRouteID: "b",
      routeOptions: [
        DesignRoute(
          id: "a", name: "Work", detail: "alex@studio.example", symbol: "person",
          kind: .account, providerID: "claude"),
        selected,
        DesignRoute(
          id: "c", name: "WORK", detail: "alex@studio.example", symbol: "person",
          kind: .account, providerID: "Claude"),
      ],
      fastModel: "", reasoningModel: "", useCompatibleFallback: true)
    let routes = compatibleAccountRoutes(in: app)
    XCTAssertEqual(routes.count, 1)
    XCTAssertEqual(routes.first?.id, "b")
  }

  func testCompatibleRoutesKeepDifferentProvidersSeparate() {
    let app = DesignApp(
      id: "codex", name: "Codex", detail: "", symbol: "s", status: .ready,
      accountCount: 2, selectedRouteID: "a",
      routeOptions: [
        DesignRoute(
          id: "a", name: "Work", detail: "alex@studio.example", symbol: "p",
          kind: .account, providerID: "claude"),
        DesignRoute(
          id: "b", name: "Work", detail: "alex@studio.example", symbol: "p",
          kind: .account, providerID: "codex"),
      ],
      fastModel: "", reasoningModel: "", useCompatibleFallback: true)
    XCTAssertEqual(compatibleAccountRoutes(in: app).count, 2)
  }

  // MARK: - P1-02 identity

  func testAccountIdentityLineHasNoDanglingSeparator() {
    XCTAssertEqual(accountIdentityLine(provider: "Claude", detail: ""), "Claude")
    XCTAssertEqual(accountIdentityLine(provider: "", detail: "alex@example.com"), "alex@example.com")
    XCTAssertEqual(accountIdentityLine(provider: "", detail: ""), "")
    XCTAssertEqual(
      accountIdentityLine(provider: "Claude", detail: "alex@example.com"),
      "Claude · alex@example.com")
  }

  func testAccountAccessibilityLabelDistinguishesDuplicates() {
    let first = DesignAccount(
      id: "1", name: "Work", detail: "alex@studio.example", provider: "Claude",
      symbol: "s", kind: .saved, status: .ready, lastUsed: "")
    let second = DesignAccount(
      id: "2", name: "Work", detail: "maya@studio.example", provider: "Codex",
      symbol: "s", kind: .saved, status: .ready, lastUsed: "")
    let all = [first, second]
    let firstLabel = accountAccessibilityLabel(for: first, in: all)
    let secondLabel = accountAccessibilityLabel(for: second, in: all)
    XCTAssertNotEqual(firstLabel, secondLabel)
    XCTAssertTrue(firstLabel.contains("Claude"))
    XCTAssertTrue(secondLabel.contains("Codex"))
    XCTAssertTrue(firstLabel.contains("1 of 2"))
    XCTAssertFalse(accountIdentityLine(provider: "Claude", detail: "").hasSuffix("·"))
  }

  // MARK: - P1-03 friendly copy

  func testFriendlyRouteDetailHidesInternalKinds() {
    XCTAssertEqual(friendlyRouteDetail(nil, routeKind: "direct-account"), "Direct account")
    XCTAssertEqual(friendlyRouteDetail("account", routeKind: "account"), "Saved account")
    XCTAssertEqual(friendlyRouteDetail("", routeKind: "gateway"), "API gateway")
    XCTAssertEqual(friendlyRouteDetail("Shared models", routeKind: "hub"), "Shared models")
    for raw in ["direct-account", "account", "provider/internal-id proxy"] {
      XCTAssertFalse(friendlyRouteDetail(raw, routeKind: raw).contains("/"))
      XCTAssertFalse(["direct-account", "account"].contains(friendlyRouteDetail(raw, routeKind: raw)))
    }
  }

  func testFriendlyProxyNameHidesCompositeID() {
    XCTAssertEqual(
      friendlyProxyName(label: "claude/abc123 proxy", providerId: "claude"), "Claude proxy")
    XCTAssertEqual(friendlyProxyName(label: "Claude Relay", providerId: "claude"), "Claude Relay")
  }

  // MARK: - P1-04 transport

  func testTransportNeverDirectWhenUnconfigured() {
    let app = DesignApp(
      id: "claude", name: "Claude Code", detail: "No login chosen", symbol: "s",
      status: .stopped, accountCount: 0, selectedRouteID: "",
      routeOptions: [], fastModel: "", reasoningModel: "", useCompatibleFallback: true)
    XCTAssertEqual(transportDisplayName(for: app), "Not Configured")
    XCTAssertFalse(isAppRouteConfigured(app))
  }

  // MARK: - P1-10 hub gating

  func testHubStartRequiresUsableModel() {
    XCTAssertEqual(usablePublishedModelCount([]), 0)
    let waiting = [
      DesignPublishedModel(
        modelID: "m", ownerAccountID: nil, ownerName: "Needs a choice",
        ownerProvider: "Claude", needsChoice: true)
    ]
    XCTAssertEqual(usablePublishedModelCount(waiting), 0)
    let usable = [
      DesignPublishedModel(
        modelID: "m", ownerAccountID: "1", ownerName: "Work",
        ownerProvider: "Claude", needsChoice: false)
    ]
    XCTAssertEqual(usablePublishedModelCount(usable), 1)
  }

  // MARK: - P1-05 activity labels stay per-event

  func testActivityAccessibilityLabelContainsDayTimeStatus() {
    let item = DesignActivity(
      id: "a1", day: "Today", time: "09:42", title: "Claude Code now uses Work",
      detail: "Applied.", symbol: "s", status: .ready, provider: "Claude")
    let label = activityAccessibilityLabel(for: item)
    XCTAssertTrue(label.contains("Today"))
    XCTAssertTrue(label.contains("09:42"))
    XCTAssertTrue(label.contains("Ready"))
    XCTAssertTrue(label.contains("Claude Code now uses Work"))
  }

  // MARK: - Dense fixtures

  func testLongNamesAndTwentyAccountsStayDistinguishable() {
    let longName = String(repeating: "Very Long Account Name ", count: 6)
    var accounts: [DesignAccount] = []
    for index in 0..<22 {
      accounts.append(
        DesignAccount(
          id: "a-\(index)", name: index < 2 ? longName : "Account \(index)",
          detail: "user\(index)@example.com", provider: index % 2 == 0 ? "Claude" : "Codex",
          symbol: "s", kind: .saved, status: .ready, lastUsed: ""))
    }
    let labels = Set(accounts.map { accountAccessibilityLabel(for: $0, in: accounts) })
    XCTAssertEqual(labels.count, accounts.count)
  }

  // MARK: - Accounts C2 gateway Test enablement

  func testGatewayTestStaysEnabledWhenNoApiKeyIsRequired() {
    // Grok reads ~/.grok/auth.json: empty key field must not disable Test.
    XCTAssertTrue(
      gatewayConnectionTestEnabled(requiresApiKey: false, apiKey: "", isBusy: false))
    XCTAssertFalse(
      gatewayConnectionTestEnabled(requiresApiKey: false, apiKey: "", isBusy: true))
    XCTAssertFalse(
      gatewayConnectionTestEnabled(requiresApiKey: true, apiKey: "   ", isBusy: false))
    XCTAssertTrue(
      gatewayConnectionTestEnabled(requiresApiKey: true, apiKey: "sk-live", isBusy: false))
    XCTAssertFalse(
      gatewayConnectionTestEnabled(requiresApiKey: true, apiKey: "sk-live", isBusy: true))
  }

  // MARK: - Accounts C9 detect-message parsing

  func testDetectMessageParsingSurvivesIdentityContainingIn() {
    XCTAssertEqual(
      accountIdentity(fromDetectionMessage: "Detected alex@studio.example in Claude Code"),
      "alex@studio.example")
    XCTAssertEqual(
      accountIdentity(fromDetectionMessage: "Detected a in b in Claude Code"),
      "a in b")
    XCTAssertEqual(
      accountIdentity(fromDetectionMessage: "Detected the current login in Codex"),
      "Current login")
    XCTAssertNil(accountIdentity(fromDetectionMessage: nil))
    XCTAssertNil(accountIdentity(fromDetectionMessage: "no prefix here"))
    XCTAssertNil(accountIdentity(fromDetectionMessage: "Detected no-source-here"))
    XCTAssertNil(accountIdentity(fromDetectionMessage: "Detected  in Claude Code"))
  }

  // MARK: - Accounts C8 submit without parsed identity

  func testSavedAccountSubmitAcceptsLiveLoginWithoutIdentity() {
    // WorkBuddy token with no `sub`: live login present, identity empty.
    XCTAssertTrue(
      addSavedAccountCanSubmit(
        name: "Work", isNewAccountMode: false, hasIdentity: false, hasLiveLogin: true,
        methodAvailable: false, providerEnabled: true, isBlocked: false))
    XCTAssertFalse(
      addSavedAccountCanSubmit(
        name: "Work", isNewAccountMode: false, hasIdentity: false, hasLiveLogin: false,
        methodAvailable: false, providerEnabled: true, isBlocked: false))
    XCTAssertFalse(
      addSavedAccountCanSubmit(
        name: "   ", isNewAccountMode: false, hasIdentity: true, hasLiveLogin: true,
        methodAvailable: true, providerEnabled: true, isBlocked: false))
    XCTAssertFalse(
      addSavedAccountCanSubmit(
        name: "Work", isNewAccountMode: false, hasIdentity: true, hasLiveLogin: false,
        methodAvailable: false, providerEnabled: false, isBlocked: false))
    XCTAssertFalse(
      addSavedAccountCanSubmit(
        name: "Work", isNewAccountMode: false, hasIdentity: true, hasLiveLogin: false,
        methodAvailable: true, providerEnabled: true, isBlocked: true))
  }

  func testNewAccountSignInStillRequiresAnAvailableMethod() {
    XCTAssertTrue(
      addSavedAccountCanSubmit(
        name: "Work", isNewAccountMode: true, hasIdentity: false, hasLiveLogin: false,
        methodAvailable: true, providerEnabled: true, isBlocked: false))
    XCTAssertFalse(
      addSavedAccountCanSubmit(
        name: "Work", isNewAccountMode: true, hasIdentity: false, hasLiveLogin: false,
        methodAvailable: false, providerEnabled: true, isBlocked: false))
  }

  func testCredentialInputSubmitRequiresNameAndSecret() {
    XCTAssertTrue(
      addCredentialInputCanSubmit(
        name: "Work", secret: "sk-live", providerEnabled: true, isBlocked: false))
    XCTAssertFalse(
      addCredentialInputCanSubmit(
        name: "Work", secret: "  ", providerEnabled: true, isBlocked: false))
    XCTAssertFalse(
      addCredentialInputCanSubmit(
        name: "", secret: "sk-live", providerEnabled: true, isBlocked: false))
    XCTAssertFalse(
      addCredentialInputCanSubmit(
        name: "Work", secret: "sk-live", providerEnabled: false, isBlocked: false))
  }

  // MARK: - Hub H5 choice resolves the conflict away
  //
  // A pick does not linger on "Choose": selectRoutingCandidate records the
  // owner and the group publishes to it, so the conflict row disappears and
  // the model appears owned. These pin the compile() branch that verdict
  // rests on — delete the owner-publish branch and both fail. The resolving
  // spinner (H7) covers the async window in production.

  func testChoosingCandidatePublishesModelAndClearsConflict() {
    let sources = [
      DesignHubSource(
        id: "a", name: "A", detail: "", provider: "Claude", symbol: "s", enabled: true),
      DesignHubSource(
        id: "b", name: "B", detail: "", provider: "Claude", symbol: "s", enabled: true),
    ]
    let catalogs = [
      DesignHubCatalog(accountID: "a", catalogID: "claude", models: ["m1"]),
      DesignHubCatalog(accountID: "b", catalogID: "claude", models: ["m1"]),
    ]
    let unchosen = DesignHubRouting.compile(sources: sources, catalogs: catalogs, owners: [:])
    XCTAssertEqual(unchosen.conflicts.count, 1)
    XCTAssertTrue(
      unchosen.models.contains(where: { $0.modelID == "m1" && $0.needsChoice }))

    let chosen = DesignHubRouting.compile(
      sources: sources, catalogs: catalogs, owners: ["m1": "b"])
    XCTAssertTrue(chosen.conflicts.isEmpty)
    let published = chosen.models.first(where: { $0.modelID == "m1" })
    XCTAssertEqual(published?.ownerAccountID, "b")
    XCTAssertEqual(published?.needsChoice, false)
  }

  func testPartialChoiceLeavesOnlyUnownedModelsInConflict() {
    let sources = [
      DesignHubSource(
        id: "a", name: "A", detail: "", provider: "Claude", symbol: "s", enabled: true),
      DesignHubSource(
        id: "b", name: "B", detail: "", provider: "Claude", symbol: "s", enabled: true),
    ]
    let catalogs = [
      DesignHubCatalog(accountID: "a", catalogID: "claude", models: ["m1", "m2"]),
      DesignHubCatalog(accountID: "b", catalogID: "claude", models: ["m1", "m2"]),
    ]
    let partial = DesignHubRouting.compile(
      sources: sources, catalogs: catalogs, owners: ["m1": "a"])
    XCTAssertEqual(partial.conflicts.count, 1)
    XCTAssertEqual(partial.conflicts[0].models, ["m2"])
    // m2 is genuinely unchosen: "Choose" is the correct selection.
    XCTAssertNil(partial.conflicts[0].selectedCandidateID)
  }

  // MARK: - Hub H6 schemeless port parsing

  func testServicePortParsesSchemelessLoopbackAddress() {
    XCTAssertEqual(servicePort(from: "http://127.0.0.1:41401"), 41401)
    XCTAssertEqual(servicePort(from: "127.0.0.1:41401"), 41401)
    XCTAssertEqual(servicePort(from: "[::1]:5150"), 5150)
    XCTAssertEqual(servicePort(from: "http://127.0.0.1:41401/health"), 41401)
    XCTAssertNil(servicePort(from: nil))
    XCTAssertNil(servicePort(from: ""))
    XCTAssertNil(servicePort(from: "localhost"))
    XCTAssertNil(servicePort(from: "http://localhost:notaport"))
  }

  // MARK: - Hub H7 row operation keys

  func testHubRowServiceKeysAreNamespaced() {
    XCTAssertEqual(hubSourceServiceID("claude-work"), "hub-source/claude-work")
    XCTAssertEqual(hubConflictServiceID("hub-source-choice-1"), "hub-conflict/hub-source-choice-1")
    XCTAssertFalse(hubSourceServiceID("x").hasPrefix("hub-conflict/"))
  }

  // MARK: - Apps A8 gateway action availability

  func testAddGatewayAvailableForClientsWithoutFirstPartyMatch() {
    // Kiro/Grok/OpenCode/WorkBuddy have no first-party gateway match; the
    // sheet's provider picker covers them, so the action stays enabled.
    XCTAssertTrue(canAddGateway(isBusy: false, gatewayProviderCount: 3))
    XCTAssertFalse(canAddGateway(isBusy: true, gatewayProviderCount: 3))
    XCTAssertFalse(canAddGateway(isBusy: false, gatewayProviderCount: 0))
  }

  // MARK: - Apps A11 attention placeholder

  private func placeholderAction(id: String, clientID: String, enabled: Bool) -> ActionSnapshot {
    ActionSnapshot(
      id: id, clientId: clientID, sourceId: "s", client: clientID, label: id,
      detail: nil, kind: "account", presentation: "app-route", selected: false,
      enabled: enabled, disabledReason: nil, routeKind: "account", modelId: nil,
      upstreamProviderId: nil, upstreamSourceLabel: nil)
  }

  private func placeholderProvider(clientID: String?, installed: Bool) -> AccountProviderSnapshot
  {
    AccountProviderSnapshot(
      id: clientID ?? "p", providerId: "claude", clientId: clientID, sourceId: nil,
      label: "Claude", detail: "", installed: installed, livePresent: nil,
      liveIdentity: nil, loginMethods: nil, canClear: nil, credentialKinds: nil)
  }

  func testAttentionPlaceholderForAllDisabledActions() {
    let disabled = [placeholderAction(id: "r1", clientID: "kiro", enabled: false)]
    XCTAssertTrue(
      appNeedsAttentionPlaceholder(
        clientID: "kiro", actions: disabled,
        providers: [placeholderProvider(clientID: "kiro", installed: false)]))
    XCTAssertFalse(
      appNeedsAttentionPlaceholder(
        clientID: "kiro",
        actions: disabled + [placeholderAction(id: "r2", clientID: "kiro", enabled: true)],
        providers: []))
    XCTAssertFalse(
      appNeedsAttentionPlaceholder(
        clientID: "kiro", actions: disabled,
        providers: [placeholderProvider(clientID: "kiro", installed: true)]))
    XCTAssertFalse(
      appNeedsAttentionPlaceholder(clientID: "ghost", actions: disabled, providers: []))
  }

  // MARK: - Apps A12 cross-provider direct-account

  func testCrossProviderDirectAccountIsARoute() {
    XCTAssertEqual(
      appRouteKind(routeKind: "direct-account", upstreamProviderID: "claude", clientID: "claude"),
      .native)
    XCTAssertEqual(
      appRouteKind(
        routeKind: "direct-account", upstreamProviderID: "anthropic", clientID: "claude"),
      .native)
    XCTAssertEqual(
      appRouteKind(routeKind: "direct-account", upstreamProviderID: "grok", clientID: "claude"),
      .account)
    XCTAssertEqual(
      appRouteKind(routeKind: "direct-account", upstreamProviderID: nil, clientID: "claude"),
      .native)
    XCTAssertEqual(
      appRouteKind(routeKind: "direct-account", upstreamProviderID: "  ", clientID: "claude"),
      .native)
    XCTAssertEqual(
      appRouteKind(routeKind: "hub", upstreamProviderID: "claude", clientID: "claude"), .hub)
    XCTAssertEqual(
      appRouteKind(routeKind: "gateway", upstreamProviderID: "openrouter", clientID: "codex"),
      .gateway)
    XCTAssertEqual(
      appRouteKind(routeKind: "pool", upstreamProviderID: "claude", clientID: "codex"), .account)
    XCTAssertEqual(
      appRouteKind(routeKind: "mystery", upstreamProviderID: "claude", clientID: "claude"),
      .account)
  }

  // MARK: - Accounts C6/C10 inline blocker reason

  func testBlockerMessageExplainsDisabledSignIn() {
    XCTAssertNil(
      addAccountBlockerMessage(
        provider: DesignProviderOption(
          id: "claude", label: "Claude", detail: "", enabled: true,
          loginMethods: [DesignLoginMethod(
            id: "oauth", label: "OAuth", detail: "", provenance: "official",
            preferred: true, available: true)]),
        method: nil,
        isNewAccountMode: false))
    let noProvider = addAccountBlockerMessage(
      provider: nil, method: nil, isNewAccountMode: false)
    XCTAssertNotNil(noProvider)
    XCTAssertTrue(noProvider?.contains("No compatible providers") == true)
    let disabled = addAccountBlockerMessage(
      provider: DesignProviderOption(
        id: "kiro", label: "Kiro", detail: "Install the kiro-cli to sign in.", enabled: false),
      method: nil,
      isNewAccountMode: false)
    XCTAssertEqual(disabled, "Install the kiro-cli to sign in.")
    let methodReason = addAccountBlockerMessage(
      provider: DesignProviderOption(id: "codex", label: "Codex", detail: "", enabled: true),
      method: DesignLoginMethod(
        id: "device", label: "Device", detail: "Browser flow", provenance: "official",
        preferred: true, available: false, unavailableReason: "Missing codex binary"),
      isNewAccountMode: true)
    XCTAssertEqual(methodReason, "Missing codex binary")
  }

  // MARK: - Activity L5 clear vs rotation

  func testVisibleLogLinesHideByCountAndResetOnRotation() {
    var hidden = 3
    XCTAssertEqual(
      visibleLogLines(["l1", "l2", "l3", "l4", "l5"], hiddenCount: &hidden),
      ["l4", "l5"])
    XCTAssertEqual(hidden, 3)

    // Rotation/restart: tail shorter than what was hidden → reset, new
    // entries visible instead of hidden forever.
    var rotated = 5
    XCTAssertEqual(visibleLogLines(["n1", "n2"], hiddenCount: &rotated), ["n1", "n2"])
    XCTAssertEqual(rotated, 0)

    // Same-length tail keeps hiding (still cleared, not a rotation —
    // only a byte offset would tell a same-length restart apart).
    var sameLength = 2
    XCTAssertEqual(visibleLogLines(["x", "y"], hiddenCount: &sameLength), [])
    XCTAssertEqual(sameLength, 2)

    // Suffix limit applies after hiding.
    var capped = 1
    let many = (1...200).map { "line\($0)" }
    XCTAssertEqual(visibleLogLines(many, hiddenCount: &capped).count, 160)
    XCTAssertEqual(visibleLogLines(many, hiddenCount: &capped).first, "line41")

    var cleared = 0
    XCTAssertEqual(visibleLogLines([], hiddenCount: &cleared), [])
  }

  // MARK: - Backups B7 all-skipped selection

  func testBackupImportSkipsReplaceWithoutOverwrite() {
    let dispositions = ["a": "ready", "b": "replace", "c": "blocked"]
    XCTAssertEqual(
      backupImportEntryIDs(
        selectedIDs: ["a", "b", "c"], dispositions: dispositions, overwrite: false),
      ["a", "c"])
    XCTAssertEqual(
      backupImportEntryIDs(
        selectedIDs: ["a", "b", "c"], dispositions: dispositions, overwrite: true),
      ["a", "b", "c"])
    // Skip-with-all-replace: empty means "nothing would change" — the caller
    // warns instead of sitting open with no feedback.
    XCTAssertEqual(
      backupImportEntryIDs(selectedIDs: ["b"], dispositions: dispositions, overwrite: false),
      [])
    XCTAssertEqual(
      backupImportEntryIDs(selectedIDs: [], dispositions: dispositions, overwrite: false),
      [])
  }

  // MARK: - Overview O1 attention covers accounts

  private func overviewProxy(id: String, status: DesignStatus) -> DesignProxy {
    DesignProxy(
      id: id, name: id, detail: "", symbol: "s", provider: "claude",
      status: status, port: 0, enabled: status == .running)
  }

  private func overviewApp(id: String, status: DesignStatus, routes: [DesignRoute] = []) -> DesignApp
  {
    DesignApp(
      id: id, name: id, detail: "", symbol: "s", status: status, accountCount: routes.count,
      selectedRouteID: routes.first?.id ?? "", routeOptions: routes,
      fastModel: "", reasoningModel: "", useCompatibleFallback: true)
  }

  private func overviewAccount(
    id: String, kind: DesignAccountKind = .saved, status: DesignStatus = .ready
  ) -> DesignAccount {
    DesignAccount(
      id: id, name: id, detail: "", provider: "Claude", symbol: "s",
      kind: kind, status: status, lastUsed: "")
  }

  private func overviewConflict(id: String, kind: DesignRoutingConflictKind) -> DesignRoutingConflict
  {
    DesignRoutingConflict(
      id: id, kind: kind, title: id, detail: "", models: ["m"], needsAttention: true,
      candidates: [])
  }

  func testAttentionTargetCoversSavedAccounts() {
    // O1 regression: account-only attention used to select nothing.
    XCTAssertEqual(
      overviewAttentionTarget(
        proxies: [overviewProxy(id: "p", status: .running)],
        apps: [overviewApp(id: "a", status: .ready)],
        accounts: [overviewAccount(id: "w", status: .attention)],
        conflicts: []),
      .account(id: "w"))
    // Priority: proxies → apps → accounts → conflicts.
    XCTAssertEqual(
      overviewAttentionTarget(
        proxies: [overviewProxy(id: "p", status: .attention)],
        apps: [overviewApp(id: "a", status: .attention)],
        accounts: [overviewAccount(id: "w", status: .attention)],
        conflicts: [overviewConflict(id: "c", kind: .sourceChoice)]),
      .proxy(id: "p"))
    XCTAssertEqual(
      overviewAttentionTarget(
        proxies: [], apps: [], accounts: [],
        conflicts: [
          overviewConflict(id: "o", kind: .modelOverlap),
          overviewConflict(id: "c", kind: .sourceChoice),
        ]),
      .conflict(kind: .sourceChoice))
    XCTAssertNil(
      overviewAttentionTarget(
        proxies: [overviewProxy(id: "p", status: .running)],
        apps: [], accounts: [overviewAccount(id: "w")], conflicts: []))
  }

  // MARK: - Overview O2/O3 checklist predicates

  func testSavedLoginExcludesGateways() {
    XCTAssertFalse(overviewHasSavedLogin(accounts: []))
    XCTAssertFalse(
      overviewHasSavedLogin(accounts: [
        overviewAccount(id: "g", kind: .gateway, status: .ready)
      ]))
    XCTAssertTrue(
      overviewHasSavedLogin(accounts: [
        overviewAccount(id: "g", kind: .gateway),
        overviewAccount(id: "w", kind: .saved),
      ]))
  }

  func testChosenRouteRequiresManagedSelection() {
    let native = DesignRoute(
      id: "native-claude", name: "Direct", detail: "", symbol: "s",
      kind: .native, providerID: "claude")
    let saved = DesignRoute(
      id: "r1", name: "Work", detail: "", symbol: "s",
      kind: .account, providerID: "claude")
    XCTAssertTrue(
      overviewHasChosenRoute(apps: [overviewApp(id: "a", status: .ready, routes: [saved, native])]))
    // Native-only login is setup still to do — in fixture and production.
    XCTAssertFalse(
      overviewHasChosenRoute(apps: [overviewApp(id: "a", status: .ready, routes: [native])]))
    XCTAssertFalse(overviewHasChosenRoute(apps: [overviewApp(id: "a", status: .ready)]))
    // Stale selection missing from the options counts for nothing.
    var stale = overviewApp(id: "a", status: .ready, routes: [saved])
    stale.selectedRouteID = "ghost"
    XCTAssertFalse(overviewIsChosenRoute(stale))
  }

  // MARK: - Overview O4/O7 service counts and summary

  func testServiceTotalSkipsPhantomHub() {
    XCTAssertEqual(
      overviewServiceTotal(proxyCount: 0, hubSourcesEmpty: true, hubEnabled: false), 0)
    XCTAssertEqual(
      overviewServiceTotal(proxyCount: 2, hubSourcesEmpty: true, hubEnabled: false), 2)
    XCTAssertEqual(
      overviewServiceTotal(proxyCount: 2, hubSourcesEmpty: false, hubEnabled: false), 3)
    XCTAssertEqual(
      overviewServiceTotal(proxyCount: 0, hubSourcesEmpty: true, hubEnabled: true), 1)
  }

  func testConnectionSummaryKeepsProductionDetail() {
    let selected = overviewApp(
      id: "a", status: .ready,
      routes: [
        DesignRoute(
          id: "r1", name: "Work", detail: "", symbol: "s",
          kind: .account, providerID: "claude")
      ])
    XCTAssertEqual(overviewConnectionSummary(selected), "Uses Work")
    var signedIn = overviewApp(id: "a", status: .ready)
    signedIn.detail = "Signed in as alex@studio.example"
    XCTAssertEqual(overviewConnectionSummary(signedIn), "Signed in as alex@studio.example")
    XCTAssertEqual(
      overviewConnectionSummary(overviewApp(id: "a", status: .ready)), "No login chosen")
  }

  // MARK: - Proxies PX3 restart parity

  func testRestartMirrorsListRow() {
    XCTAssertTrue(canRestartProxy(enabled: true, status: .running))
    XCTAssertTrue(canRestartProxy(enabled: false, status: .attention))
    XCTAssertTrue(canRestartProxy(enabled: false, status: .unavailable))
    XCTAssertFalse(canRestartProxy(enabled: false, status: .stopped))
    XCTAssertFalse(canRestartProxy(enabled: false, status: .ready))
  }

  // MARK: - Tray T3 dismissal keys

  func testDismissalKeysOnStableID() {
    XCTAssertTrue(trayAttentionDismissed(id: "app.kiro", title: "Old copy", keys: ["app.kiro"]))
    // Legacy "id|title" keys still count: a copy change must not resurrect.
    XCTAssertTrue(
      trayAttentionDismissed(id: "app.kiro", title: "New copy", keys: ["app.kiro|Old copy"]))
    XCTAssertFalse(trayAttentionDismissed(id: "app.kiro", title: "X", keys: ["app.codex"]))
    // Pruned, and the new dismissal survives the prune.
    let full = Set((1...100).map { "k\($0)" })
    let pruned = trayDismissAttention(id: "app.kiro", keys: full)
    XCTAssertEqual(pruned.count, 100)
    XCTAssertTrue(pruned.contains("app.kiro"))
  }

  // MARK: - Tray T4 switch marks match by route id

  func testSwitchMarksMatchByRouteID() {
    let selected = "r-selected"
    XCTAssertEqual(
      traySwitchStateForRoute(
        phase: .idle, appID: "claude", routeID: selected, selectedRouteID: selected),
      .current)
    // Duplicate names across providers: the name matches, the id does not.
    XCTAssertEqual(
      traySwitchStateForRoute(
        phase: .inFlight(appID: "claude", from: "Work", to: "Work", routeID: "r-other"),
        appID: "claude", routeID: "r-work-grok", selectedRouteID: selected),
      .available)
    XCTAssertEqual(
      traySwitchStateForRoute(
        phase: .inFlight(appID: "claude", from: "Old", to: "Work", routeID: "r-work"),
        appID: "claude", routeID: "r-work", selectedRouteID: selected),
      .switching)
    XCTAssertEqual(
      traySwitchStateForRoute(
        phase: .recovery(appID: "claude", requested: "Work", kept: "Old", routeID: "r-work"),
        appID: "claude", routeID: "r-work", selectedRouteID: selected),
      .retry)
    // Another app's flight never marks this row.
    XCTAssertEqual(
      traySwitchStateForRoute(
        phase: .inFlight(appID: "codex", from: "A", to: "B", routeID: "r-work"),
        appID: "claude", routeID: "r-work", selectedRouteID: selected),
      .available)
  }

  // MARK: - Tray T6 first unused name

  func testFirstRunSaveUsesFirstUnusedName() {
    XCTAssertEqual(unusedAccountName(existing: []), "Work")
    XCTAssertEqual(unusedAccountName(existing: ["Work"]), "Personal")
    XCTAssertEqual(unusedAccountName(existing: ["work", "PERSONAL"]), "Account 3")
  }

  // MARK: - Brand marks render full color

  func testChromaticMarksRenderOriginal() {    for file in [
      "claude-mark-orange.svg", "deepseek.svg", "doubao.svg", "gemini-icon-2025.svg",
      "hunyuan.svg", "mimo.svg", "minimax.svg", "mistral.svg", "modelscope.svg",
      "nvidia.svg", "qwen.svg", "kiro-app.png", "workbuddy-mark.png", "zcode-app.png",
    ] {
      XCTAssertTrue(DesignBrandAsset.rendersOriginal(file), file)
    }
    for file in [
      "claude.svg", "kimi.svg", "opencode.svg", "zai.svg", "openai.svg",
      "grok-mark-dark.png", "openai-mark-light.png",
    ] {
      XCTAssertFalse(DesignBrandAsset.rendersOriginal(file), file)
    }
  }

  // MARK: - Activity L9 provider decode

  func testActivityEventDecodesProvider() throws {
    let data = try JSONSerialization.data(withJSONObject: [
      "id": "e1",
      "createdAt": "2026-09-05T12:00:00.000Z",
      "message": "Codex now uses grok/a.",
      "isError": false,
      "kind": "activation",
      "provider": "grok",
    ])
    XCTAssertEqual(try JSONDecoder().decode(ActivityEvent.self, from: data).provider, "grok")
    let legacy = try JSONSerialization.data(withJSONObject: [
      "id": "e2",
      "createdAt": "2026-09-05T12:00:00.000Z",
      "message": "Operation completed.",
      "isError": false,
      "kind": "activation",
    ])
    XCTAssertNil(try JSONDecoder().decode(ActivityEvent.self, from: legacy).provider)
  }

  // MARK: - ZCode plan warning

  func testZcodePlanWarning() throws {
    // Mirrors ~/.zcode/v2/coding-plan-cache.json: all unavailable → warn.
    let stale = try JSONSerialization.data(withJSONObject: [
      "version": 1,
      "entryStatus": ["items": [
        "builtin:zai-coding-plan": ["status": "unavailable", "reason": "coding_plan_not_authenticated"]
      ]],
    ])
    let warning = zcodeCodingPlanWarning(stale)
    XCTAssertNotNil(warning)
    XCTAssertTrue(warning?.contains("can’t be imported") == true)
    let connected = try JSONSerialization.data(withJSONObject: [
      "version": 1,
      "entryStatus": ["items": [
        "builtin:zai-coding-plan": ["status": "connected"]
      ]],
    ])
    XCTAssertNil(zcodeCodingPlanWarning(connected))
    XCTAssertNil(zcodeCodingPlanWarning(Data("garbage".utf8)))
    XCTAssertNil(zcodeCodingPlanWarning(Data("{}".utf8)))
  }

  // MARK: - Usage window cadence

  func testUsageWindowPeriodNamesCadence() {
    XCTAssertEqual(usageWindowPeriod(limitWindowSeconds: 18000), "5 hours")
    XCTAssertEqual(usageWindowPeriod(limitWindowSeconds: 604800), "week")
    XCTAssertEqual(usageWindowPeriod(limitWindowSeconds: 3600), "1 hour")
    XCTAssertEqual(usageWindowPeriod(limitWindowSeconds: 86400), "day")
    XCTAssertEqual(usageWindowPeriod(limitWindowSeconds: 1209600), "2 weeks")
    XCTAssertEqual(usageWindowPeriod(limitWindowSeconds: 90), "2 minutes")
    XCTAssertNil(usageWindowPeriod(limitWindowSeconds: nil))
    XCTAssertNil(usageWindowPeriod(limitWindowSeconds: 0))
    XCTAssertNil(usageWindowPeriod(limitWindowSeconds: -5))
  }

  // MARK: - Edit-sheet wait race (A1)

  @MainActor
  func testEditAccountRefusedWhileBusySendsNothingToWaitOn() async throws {
    // The edit sheet arms its wait only on accept. A refused mutation must
    // report false and send nothing, so no result can ever arrive to end it.
    let (store, model, box) = makeProductionHarness()
    store.consume(Data((try accountSnapshotFrame() + "\n").utf8))
    await waitUntil(model.accounts.contains { $0.id == "acc-1" })
    XCTAssertTrue(model.accounts.contains { $0.id == "acc-1" })
    // Occupy the single-flight slot.
    XCTAssertNotNil(
      store.mutate(
        operation: "account-refresh", name: "Work", providerId: "claude",
        activityLabel: "refreshing Work"))
    let sentBefore = box.lines.count
    XCTAssertFalse(model.updateAccount(id: "acc-1", label: "Renamed"))
    XCTAssertEqual(box.lines.count, sentBefore)
    XCTAssertNil(model.operationResult)
  }

  @MainActor
  func testEditAccountAcceptedTracksMutation() async throws {
    let (store, model, box) = makeProductionHarness()
    store.consume(Data((try accountSnapshotFrame() + "\n").utf8))
    await waitUntil(model.accounts.contains { $0.id == "acc-1" })
    XCTAssertTrue(model.updateAccount(id: "acc-1", label: "Renamed"))
    XCTAssertEqual(box.lines.count, 1)
    XCTAssertTrue(box.lines[0].hasPrefix("mutate\t"))
    XCTAssertNotNil(store.busyRequestId)
  }

  // MARK: - Confirmed forget (B3)

  @MainActor
  func testForgetBackupFailureKeepsPreviewAndShowsError() async throws {
    let (store, model, _) = makeProductionHarness()
    try await stageBackupPreview(store)
    await waitUntil(model.backups.contains { $0.id == "insp-1" })
    XCTAssertTrue(model.backups.contains { $0.id == "insp-1" })
    // Forget travels async: the row stays until the engine confirms.
    XCTAssertTrue(model.deleteBackup(id: "insp-1"))
    XCTAssertEqual(store.backupPreview?.inspectionId, "insp-1")
    XCTAssertTrue(model.backups.contains { $0.id == "insp-1" })
    guard let forgetID = store.busyRequestId else {
      return XCTFail("forget claimed no busy slot")
    }
    store.consume(
      Data(
        (try frame(
          "result",
          ["version": 1, "requestId": forgetID, "status": "error", "message": "forget failed"]
        ) + "\n").utf8))
    await waitUntil(model.feedback?.message == "forget failed")
    XCTAssertEqual(store.backupPreview?.inspectionId, "insp-1")
    XCTAssertTrue(model.backups.contains { $0.id == "insp-1" })
    XCTAssertEqual(model.feedback?.message, "forget failed")
  }

  @MainActor
  func testForgetBackupSuccessClearsPreview() async throws {
    let (store, model, _) = makeProductionHarness()
    try await stageBackupPreview(store)
    await waitUntil(model.backups.contains { $0.id == "insp-1" })
    XCTAssertTrue(model.deleteBackup(id: "insp-1"))
    guard let forgetID = store.busyRequestId else {
      return XCTFail("forget claimed no busy slot")
    }
    store.consume(
      Data(
        (try frame(
          "result",
          ["version": 1, "requestId": forgetID, "status": "success", "message": "Forgot review."]
        ) + "\n").utf8))
    await waitUntil(store.backupPreview == nil)
    XCTAssertNil(store.backupPreview)
    await waitUntil(!model.backups.contains { $0.id == "insp-1" })
    XCTAssertFalse(model.backups.contains { $0.id == "insp-1" })
  }

  @MainActor
  func testForgetBackupRefusedWhileBusyKeepsPreview() async throws {
    let (store, model, _) = makeProductionHarness()
    try await stageBackupPreview(store)
    await waitUntil(model.backups.contains { $0.id == "insp-1" })
    XCTAssertNotNil(
      store.mutate(
        operation: "account-refresh", name: "Work", providerId: "claude",
        activityLabel: "refreshing Work"))
    XCTAssertFalse(model.deleteBackup(id: "insp-1"))
    XCTAssertEqual(store.backupPreview?.inspectionId, "insp-1")
    XCTAssertTrue(model.backups.contains { $0.id == "insp-1" })
  }

  @MainActor
  private func makeProductionHarness() -> (TrayStore, ProductionDesignModel, CommandBox) {
    let box = CommandBox()
    let store = TrayStore(initialCount: 0)
    store.attachCommandSink { box.lines.append($0) }
    return (store, ProductionDesignModel(store: store), box)
  }

  private func accountSnapshotFrame() throws -> String {
    try frame("snapshot", [
      "revision": 1,
      "proxyCount": 0,
      "routes": [],
      "actions": [],
      "usage": [],
      "proxies": [],
      "accounts": [[
        "id": "acc-1",
        "providerId": "claude",
        "name": "Work",
        "label": "Work",
        "detail": "Claude · Work",
        "active": false,
        "canRefresh": true,
        "switchActionId": "switch-1",
      ]],
      "gateways": [],
      "accountProviders": [],
      "gatewayProviders": [],
      "settings": [
        "launchAtLogin": false,
        "startEnabledProxies": true,
        "showQuota": true,
        "quotaGuardEnabled": false,
      ],
      "activity": [],
    ])
  }

  @MainActor
  private func stageBackupPreview(_ store: TrayStore) async throws {
    guard let inspectID = store.inspectBackup(path: "/tmp/review.anypick-backup") else {
      return XCTFail("inspect claimed no preview slot")
    }
    store.consume(
      Data(
        (try frame("backup-preview", [
          "version": 1,
          "requestId": inspectID,
          "status": "ready",
          "inspectionId": "insp-1",
          "createdAt": "Sep 6",
          "entries": [[
            "id": "e-1",
            "providerId": "claude",
            "name": "Work",
            "label": "Work",
            "disposition": "new",
          ]],
        ]) + "\n").utf8))
  }

  @MainActor
  private func waitUntil(_ condition: @autoclosure () -> Bool) async {
    for _ in 0..<200 {
      if condition() { return }
      try? await Task.sleep(nanoseconds: 5_000_000)
    }
  }
}

private final class CommandBox {
  var lines: [String] = []
}

// MARK: - Pane render smoke (interaction-crash gate)

/// Every destination must build its body and lay out without trapping.
/// Added after a "crash khi thao tác" report: SwiftUI body traps only
/// surface when the pane actually renders, so helper unit tests never
/// catch them. NSHostingView layout runs headless (no pixels drawn).
final class PaneRenderTests: XCTestCase {
  @MainActor
  private func render(_ view: some View) {
    let host = NSHostingView(rootView: AnyView(view))
    host.frame = NSRect(x: 0, y: 0, width: 920, height: 640)
    host.layoutSubtreeIfNeeded()
  }

  @MainActor
  func testEditedPanesRenderWithoutTrapping() {
    let model = MacOSDesignModel.fromEnvironment(["ANYPICK_MACOS_FIXTURE": "ready"])
    XCTAssertFalse(model.accounts.isEmpty, "fixture needs accounts for row coverage")
    render(AccountsView(model: model))
    render(ProxiesView(model: model))
    render(ProxyHubView(model: model))
    render(ActivityView(model: model))
    for account in model.accounts {
      render(AccountDetailView(model: model, accountID: account.id))
    }
    render(AccountDetailView(model: model, accountID: "no-such-account"))
    for app in model.apps {
      render(AppDetailView(model: model, appID: app.id))
    }
    for proxy in model.proxies {
      render(ProxyDetailView(model: model, proxyID: proxy.id))
    }
    render(ProxyDetailView(model: model, proxyID: "no-such-proxy"))
    let logsModel = MacOSDesignModel.fromEnvironment([
      "ANYPICK_MACOS_FIXTURE": "ready",
      "ANYPICK_MACOS_ACTIVITY_MODE": "logs",
    ])
    render(ActivityView(model: logsModel))
    // Both hub power states: the footer buttons attach to different
    // sections depending on conflicts, and the status row changes shape.
    model.proxyHubEnabled = false
    render(ProxyHubView(model: model))
    model.proxyHubEnabled = true
    render(ProxyHubView(model: model))
  }

  // MARK: - Model step in the connection flow

  private func modelConfigApp(
    roles: [DesignModelRole],
    options: [String],
    editable: Bool = true
  ) -> DesignApp {
    DesignApp(
      id: "claude",
      name: "Claude Code",
      detail: "Uses Work",
      symbol: "s",
      status: .ready,
      accountCount: 1,
      selectedRouteID: "r1",
      routeOptions: [
        DesignRoute(
          id: "r1", name: "Work", detail: "work@example.com", symbol: "person",
          kind: .account, providerID: "codex")
      ],
      fastModel: "",
      reasoningModel: "",
      useCompatibleFallback: true,
      modelRoles: roles,
      modelOptions: options,
      modelEditingAvailable: editable
    )
  }

  func testConnectionSheetOffersModelStepOnlyWhenTheRoutePublishesModels() {
    let roles = [DesignModelRole(id: "default", label: "Default", modelID: "")]
    // No options, no roles, or a route that manages its own picker: all three
    // would render a dead control.
    XCTAssertFalse(appRouteOffersModelStep(modelConfigApp(roles: roles, options: [])))
    XCTAssertFalse(appRouteOffersModelStep(modelConfigApp(roles: [], options: ["m1"])))
    XCTAssertFalse(
      appRouteOffersModelStep(modelConfigApp(roles: roles, options: ["m1"], editable: false)))
    XCTAssertTrue(appRouteOffersModelStep(modelConfigApp(roles: roles, options: ["m1"])))
  }

  func testConnectionSheetNudgesOnlyWhenNoDefaultIsPinned() {
    let empty = [DesignModelRole(id: "default", label: "Default", modelID: "")]
    let pinned = [DesignModelRole(id: "default", label: "Default", modelID: "m1")]
    XCTAssertTrue(appRouteNeedsModelChoice(modelConfigApp(roles: empty, options: ["m1"])))
    XCTAssertFalse(appRouteNeedsModelChoice(modelConfigApp(roles: pinned, options: ["m1"])))
  }

  @MainActor
  func testConnectionSheetRendersEveryStage() {
    let model = MacOSDesignModel.fromEnvironment(["ANYPICK_MACOS_FIXTURE": "ready"])
    for app in model.apps {
      render(AppRouteSheet(model: model, appID: app.id))
      render(AppModelsSheet(model: model, appID: app.id))
    }
  }
}
