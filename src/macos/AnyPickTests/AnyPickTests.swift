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
}
