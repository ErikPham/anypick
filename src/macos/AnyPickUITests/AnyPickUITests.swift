import AppKit
import XCTest

/// Drives the three ship-acceptance flows through the real UI on fixture data.
/// Fixture mode has no Go service and no disk access, so these runs need no
/// login and leave `~/.anypick` untouched.
final class AnyPickUITests: XCTestCase {
  private let timeout: TimeInterval = 10

  override func setUp() {
    continueAfterFailure = false
    hideOtherApps()
  }

  /// Windows of other apps above AnyPick swallow synthesized clicks, and
  /// XCTest's interruption check can throw on their elements. Hide every
  /// other app that can own a window for the run; they come back from the
  /// Dock or Cmd-Tab.
  private func hideOtherApps() {
    let me = ProcessInfo.processInfo.processIdentifier
    let others = NSWorkspace.shared.runningApplications.filter {
      $0.activationPolicy != .prohibited && !$0.isHidden
        && $0.bundleIdentifier != "dev.anypick.app"
        && $0.bundleIdentifier != "com.apple.finder"
        && $0.processIdentifier != me
    }
    others.forEach { $0.hide() }
    let deadline = Date().addingTimeInterval(3)
    while Date() < deadline, others.contains(where: { !$0.isHidden }) {
      RunLoop.current.run(until: Date().addingTimeInterval(0.1))
    }
  }

  private func launch(fixture: String, flow: String? = nil, tray: String? = nil) -> XCUIApplication {
    let app = XCUIApplication()
    app.launchEnvironment["ANYPICK_MACOS_FIXTURE"] = fixture
    if let flow { app.launchEnvironment["ANYPICK_MACOS_FLOW"] = flow }
    if let tray { app.launchEnvironment["ANYPICK_MACOS_TRAY"] = tray }
    app.launchArguments += ["-ApplePersistenceIgnoreState", "YES"]
    app.launch()
    hideOtherApps()
    // Other full-screen apps (and Stage Manager) can keep AnyPick behind them,
    // which swallows clicks and defers the Welcome sheet; bring it forward.
    app.activate()
    XCTAssertTrue(app.wait(for: .runningForeground, timeout: timeout), "AnyPick never came to the foreground")
    return app
  }

  private func element(_ app: XCUIApplication, _ id: String) -> XCUIElement {
    app.descendants(matching: .any)[id].firstMatch
  }

  private func button(_ app: XCUIApplication, _ label: String, in container: XCUIElement? = nil) -> XCUIElement {
    (container ?? app).buttons[label].firstMatch
  }

  private func waitFor(_ element: XCUIElement, _ what: String) {
    XCTAssertTrue(element.waitForExistence(timeout: timeout), "\(what) never appeared")
  }

  private func attachScreenshot(_ name: String) {
    let shot = XCTAttachment(screenshot: XCUIScreen.main.screenshot())
    shot.name = name
    shot.lifetime = .keepAlways
    add(shot)
  }

  private func dismissWelcome(_ app: XCUIApplication) {
    // The sheet title is a header whose text lives in its AX label, which the
    // subscript lookup does not match on macOS; match the label explicitly.
    let sheet = app.sheets.containing(
      NSPredicate(format: "label == %@", "Welcome to AnyPick")
    ).firstMatch
    waitFor(sheet, "Welcome sheet")
    attachScreenshot("1-welcome")
    let primary = sheet.buttons["sheet.primary"].firstMatch
    waitFor(primary, "Welcome Continue button")
    XCTAssertEqual(primary.title, "Continue")
    primary.click()
    XCTAssertTrue(sheet.waitForNonExistence(timeout: timeout), "Welcome sheet did not dismiss")
  }

  // Design step 1: First launch → Welcome → Save a Login → Point an App.
  func testFirstLaunchWelcomeThenChecklistOpensAccountsAndApps() {
    var app = launch(fixture: "empty")
    dismissWelcome(app)

    let checklist = element(app, "overview.get-started")
    waitFor(checklist, "Get Started checklist")
    let saveRow = element(app, "overview.onboarding.save-login")
    let routeRow = element(app, "overview.onboarding.choose-route")
    waitFor(saveRow, "Save a Login row")
    waitFor(routeRow, "Point an App row")
    XCTAssertEqual(saveRow.value as? String, "Not done")
    XCTAssertEqual(routeRow.value as? String, "Not done")
    attachScreenshot("1-checklist")

    // Each row collapses into its action button, which carries the row id.
    XCTAssertEqual(saveRow.label, "Save…")
    saveRow.click()
    waitFor(app.staticTexts["No Accounts"].firstMatch, "Accounts pane (No Accounts)")
    attachScreenshot("1-save-login-opened-accounts")
    app.terminate()

    // Fresh launch for the second CTA so each CTA starts from the checklist.
    app = launch(fixture: "empty")
    dismissWelcome(app)
    let routeRowAgain = element(app, "overview.onboarding.choose-route")
    waitFor(routeRowAgain, "Point an App row")
    XCTAssertEqual(routeRowAgain.label, "Choose…")
    routeRowAgain.click()
    waitFor(app.staticTexts["No Apps Configured"].firstMatch, "Apps pane (No Apps Configured)")
    waitFor(app.buttons["Add App…"].firstMatch, "Apps empty-state Add App… button")
    attachScreenshot("1-point-app-opened-apps")
    app.terminate()
  }

  // Design step 2, branch without a live login: tray first-run → Open Accounts.
  // Design step 3: Empty Accounts → Add Account… → tray leaves first run.
  func testTrayFirstRunOpenAccountsThenAddAccountLeavesFirstRun() {
    let app = launch(fixture: "empty", flow: "tray-first-run")
    let tray = app.windows["Menu Bar"].firstMatch
    waitFor(tray, "Menu Bar window")
    waitFor(tray.staticTexts["Add a login to start"].firstMatch, "tray first-run headline")
    let save = tray.descendants(matching: .any)["tray.first-run.save"].firstMatch
    waitFor(save, "tray.first-run.save")
    XCTAssertTrue(tray.buttons["Open Accounts"].firstMatch.exists, "first-run CTA is not Open Accounts")
    attachScreenshot("2-tray-first-run-no-live-login")
    save.click()

    waitFor(app.staticTexts["No Accounts"].firstMatch, "Accounts empty state after Open Accounts")
    attachScreenshot("3-empty-accounts")
    button(app, "Add Account…").click()

    let source = element(app, "account-flow.source")
    waitFor(element(app, "account-flow.save"), "Add Account sheet")
    if source.exists {
      let newAccount = source.radioButtons["New Account"].firstMatch
      if newAccount.exists { newAccount.click() } else { source.buttons["New Account"].firstMatch.click() }
    }
    let name = element(app, "account-flow.name")
    if name.exists {
      name.click()
      name.typeText("Work")
    }
    attachScreenshot("3-add-account-sheet")
    let submit = element(app, "account-flow.save")
    XCTAssertTrue(submit.isEnabled, "Add Account primary button is disabled")
    submit.click()

    XCTAssertTrue(
      app.staticTexts["No Accounts"].firstMatch.waitForNonExistence(timeout: timeout),
      "Accounts pane still empty after Add Account")
    attachScreenshot("3-account-added")

    XCTAssertTrue(
      tray.descendants(matching: .any)["tray.first-run.save"].firstMatch
        .waitForNonExistence(timeout: timeout),
      "tray still shows first run after an account exists")
    attachScreenshot("3-tray-after-account")
    app.terminate()
  }

  // Design step 2, branch with a detected live login: Save This Login.
  // Fixture login only; the real OAuth path stays a human check.
  func testTrayFirstRunSaveThisLoginLeavesFirstRun() {
    let app = launch(fixture: "ready", flow: "tray-first-run", tray: "firstRun")
    let tray = app.windows["Menu Bar"].firstMatch
    waitFor(tray, "Menu Bar window")
    waitFor(tray.staticTexts["Save a login to start"].firstMatch, "tray first-run headline (live login)")
    let save = tray.descendants(matching: .any)["tray.first-run.save"].firstMatch
    waitFor(save, "tray.first-run.save")
    XCTAssertTrue(tray.buttons["Save This Login"].firstMatch.exists, "first-run CTA is not Save This Login")
    attachScreenshot("2-tray-first-run-live-login")
    save.click()
    XCTAssertTrue(save.waitForNonExistence(timeout: timeout), "tray stayed in first run after Save This Login")
    attachScreenshot("2-tray-after-save")
    app.terminate()
  }
}
