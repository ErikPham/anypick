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

  private func launch(fixture: String, flow: String? = nil, tray: String? = nil, openSettings: Bool = false) -> XCUIApplication {
    let app = XCUIApplication()
    app.launchEnvironment["ANYPICK_MACOS_FIXTURE"] = fixture
    if let flow { app.launchEnvironment["ANYPICK_MACOS_FLOW"] = flow }
    if let tray { app.launchEnvironment["ANYPICK_MACOS_TRAY"] = tray }
    if openSettings { app.launchEnvironment["ANYPICK_UITEST_OPEN_SETTINGS"] = "1" }
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

  /// Kept only when the test fails. XCUIScreen captures the whole display, so on a
  /// Mac someone is using a passing run must not leave other windows in the xcresult.
  private func attachScreenshot(_ name: String) {
    let shot = XCTAttachment(screenshot: XCUIScreen.main.screenshot())
    shot.name = name
    shot.lifetime = .deleteOnSuccess
    add(shot)
  }

  private func welcomeSheet(_ app: XCUIApplication) -> XCUIElement {
    // The sheet title is a header whose text lives in its AX label, which the
    // subscript lookup does not match on macOS; match the label explicitly.
    let sheet = app.sheets.containing(
      NSPredicate(format: "label == %@", "Welcome to AnyPick")
    ).firstMatch
    waitFor(sheet, "Welcome sheet")
    return sheet
  }

  /// Leaves Welcome by its secondary action, for tests that start from the
  /// Get Started checklist rather than from adding a login.
  private func dismissWelcome(_ app: XCUIApplication) {
    let sheet = welcomeSheet(app)
    let notNow = sheet.buttons["sheet.cancel"].firstMatch
    waitFor(notNow, "Welcome Not Now button")
    XCTAssertEqual(notNow.title, "Not Now")
    notNow.click()
    XCTAssertTrue(sheet.waitForNonExistence(timeout: timeout), "Welcome sheet did not dismiss")
  }

  private func waitForAddLoginSheet(_ app: XCUIApplication, _ after: String) {
    waitFor(element(app, "account-flow.save"), "Add Login sheet after \(after)")
  }

  // Design step 1: First launch → Welcome → Add Login… lands on the Add sheet,
  // not on a pane the user still has to find the button in.
  func testWelcomeAddLoginOpensAddSheet() {
    let app = launch(fixture: "empty")
    let sheet = welcomeSheet(app)
    attachScreenshot("1-welcome")
    let primary = sheet.buttons["sheet.primary"].firstMatch
    waitFor(primary, "Welcome Add Login button")
    XCTAssertEqual(primary.title, "Add Login…")
    primary.click()
    XCTAssertTrue(sheet.waitForNonExistence(timeout: timeout), "Welcome sheet did not dismiss")
    waitForAddLoginSheet(app, "Welcome")
    attachScreenshot("1-welcome-add-login-sheet")
    app.terminate()
  }

  // Design step 1, checklist path: Not Now → Add a Login opens the same sheet;
  // cancelling it leaves the empty Accounts pane, whose Add Login… opens it
  // again. Point an App opens Apps.
  func testChecklistAddLoginAndEmptyAccountsShareOneSheet() {
    var app = launch(fixture: "empty")
    dismissWelcome(app)

    let checklist = element(app, "overview.get-started")
    waitFor(checklist, "Get Started checklist")
    let saveRow = element(app, "overview.onboarding.save-login")
    let routeRow = element(app, "overview.onboarding.choose-route")
    waitFor(saveRow, "Add a Login row")
    waitFor(routeRow, "Point an App row")
    XCTAssertEqual(saveRow.value as? String, "Not done")
    XCTAssertEqual(routeRow.value as? String, "Not done")
    attachScreenshot("1-checklist")

    // Each row collapses into its action button, which carries the row id.
    XCTAssertEqual(saveRow.label, "Add Login…")
    saveRow.click()
    waitForAddLoginSheet(app, "the checklist")
    attachScreenshot("1-checklist-add-login-sheet")
    let cancel = app.sheets.buttons["sheet.cancel"].firstMatch
    waitFor(cancel, "Add Login sheet Cancel")
    cancel.click()
    waitFor(app.staticTexts["No Accounts"].firstMatch, "Accounts pane (No Accounts)")
    button(app, "Add Login…").click()
    waitForAddLoginSheet(app, "empty Accounts")
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

  // Design step 2, branch without a live login: tray first-run → Add Login….
  // Design step 3: the Add sheet saves an account → tray leaves first run.
  func testTrayFirstRunAddLoginThenAddAccountLeavesFirstRun() {
    let app = launch(fixture: "empty", flow: "tray-first-run")
    let tray = app.windows["Menu Bar"].firstMatch
    waitFor(tray, "Menu Bar window")
    waitFor(tray.staticTexts["Add a login to start"].firstMatch, "tray first-run headline")
    let save = tray.descendants(matching: .any)["tray.first-run.save"].firstMatch
    waitFor(save, "tray.first-run.save")
    XCTAssertTrue(tray.buttons["Add Login…"].firstMatch.exists, "first-run CTA is not Add Login…")
    attachScreenshot("2-tray-first-run-no-live-login")
    save.click()

    let source = element(app, "account-flow.source")
    waitForAddLoginSheet(app, "the tray")
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
    XCTAssertTrue(submit.isEnabled, "Add Login sheet primary button is disabled")
    submit.click()

    XCTAssertTrue(
      app.staticTexts["No Accounts"].firstMatch.waitForNonExistence(timeout: timeout),
      "Accounts pane still empty after adding an account")
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

  // Settings smoke (QA): one run covers tabs, one control per pane, and the
  // About version string that General and the app menu both read from the bundle.
  func testSettingsPanesAndAboutVersion() {
    // Open path is fixture-only (ANYPICK_UITEST_OPEN_SETTINGS → openDesignSettings).
    // Menu/⌘, against SwiftUI Settings { } is flaky under XCUITest; content asserts stay.
    let app = launch(fixture: "ready", openSettings: true)
    let welcome = app.sheets.containing(
      NSPredicate(format: "label == %@", "Welcome to AnyPick")
    ).firstMatch
    if welcome.waitForExistence(timeout: 2) {
      dismissWelcome(app)
    }

    let settingsWindow = app.windows["Settings"].firstMatch
    waitFor(settingsWindow, "Settings window")
    waitFor(element(app, "settings.getting-started"), "Settings General (settings.getting-started)")
    let tabs = element(app, "settings.tabs")
    waitFor(tabs, "Settings sidebar (settings.tabs)")
    attachScreenshot("settings-general")

    let expectedTabs = ["General", "Usage", "Proxies", "Security", "Diagnostics"]
    for name in expectedTabs {
      XCTAssertTrue(
        tabs.staticTexts[name].firstMatch.exists || tabs.buttons[name].firstMatch.exists
          || app.outlines.staticTexts[name].firstMatch.exists
          || app.tables.staticTexts[name].firstMatch.exists,
        "Settings sidebar missing \(name)")
    }

    func selectTab(_ name: String) {
      let candidates: [XCUIElement] = [
        tabs.staticTexts[name].firstMatch,
        tabs.buttons[name].firstMatch,
        app.outlines.staticTexts[name].firstMatch,
        app.tables.cells.staticTexts[name].firstMatch,
        app.tables.staticTexts[name].firstMatch,
      ]
      let hit = candidates.first { $0.exists }
      XCTAssertNotNil(hit, "no sidebar control for \(name)")
      hit?.click()
      // Navigation title lands on the Settings window.
      waitFor(app.staticTexts[name].firstMatch, "\(name) navigation title")
    }

    selectTab("General")
    waitFor(element(app, "settings.getting-started"), "settings.getting-started")
    let version = element(app, "settings.version")
    waitFor(version, "settings.version")
    let short = marketingVersion(of: app)
    XCTAssertEqual(
      version.label.isEmpty ? (version.value as? String ?? "") : version.label,
      "Version \(short)",
      "General About must match CFBundleShortVersionString")

    selectTab("Usage")
    waitFor(element(app, "settings.quota-guard"), "settings.quota-guard")

    selectTab("Proxies")
    waitFor(element(app, "settings.open-proxies"), "settings.open-proxies")

    selectTab("Security")
    waitFor(app.staticTexts["Hide Tokens"].firstMatch, "Hide Tokens row")
    waitFor(app.staticTexts["Local Access Only"].firstMatch, "Local Access Only row")
    waitFor(app.staticTexts["Account Backups"].firstMatch, "Account Backups row")
    attachScreenshot("settings-security")

    selectTab("Diagnostics")
    waitFor(element(app, "settings.run-doctor"), "settings.run-doctor")

    // Menu About AnyPick shows the same marketing version (no build number).
    let aboutItem = app.menuItems["About AnyPick"].firstMatch
    waitFor(aboutItem, "About AnyPick menu item")
    aboutItem.click()
    let about = app.dialogs.firstMatch.exists ? app.dialogs.firstMatch : app.windows.matching(
      NSPredicate(format: "title CONTAINS %@", "AnyPick")
    ).element(boundBy: 0)
    // Standard about panel surfaces the marketing version as static text.
    let versionInAbout = about.staticTexts.matching(
      NSPredicate(format: "label CONTAINS %@ OR value CONTAINS %@", short, short)
    ).firstMatch
    waitFor(versionInAbout, "About panel version \(short)")
    attachScreenshot("settings-about-panel")
    app.terminate()
  }

  /// CFBundleShortVersionString of the app under test — the same source General
  /// and About AnyPick read. Stays free of a hardcoded 1.1.0 until set-version.
  private func marketingVersion(of _: XCUIApplication) -> String {
    // Same bundle id hideOtherApps filters on; XCUIApplication.bundleID is not
    // available on every macOS XCTest SDK this project builds against.
    let productBundleID = "dev.anypick.app"
    let matches = NSWorkspace.shared.runningApplications.filter {
      $0.bundleIdentifier == productBundleID
    }
    XCTAssertFalse(matches.isEmpty, "no running app for bundle \(productBundleID)")
    let url = matches[0].bundleURL
    XCTAssertNotNil(url, "running app has no bundleURL")
    let short = Bundle(url: url!)?.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String
    XCTAssertNotNil(short.flatMap { $0.isEmpty ? nil : $0 }, "CFBundleShortVersionString missing")
    return short!
  }
}
