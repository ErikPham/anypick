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
}
