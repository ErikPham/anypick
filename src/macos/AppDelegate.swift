import AppKit
import Darwin
import Foundation
import SwiftUI

@MainActor
final class AnyPickAppDelegate: NSObject, NSApplicationDelegate {
  private static weak var instance: AnyPickAppDelegate?

  let isFixture: Bool
  let store: TrayStore?
  let designModel: MacOSDesignModel
  private var serviceProcess: GoServiceProcess?
  private var terminationPending = false
  /// Tray Quit (and `quitAnyPick`) set this so Dock/⌘Q can hide to the
  /// menu bar instead of killing the process.
  private var allowTerminate = false

  override init() {
    let environment = ProcessInfo.processInfo.environment
    let fixture = environment["ANYPICK_MACOS_FIXTURE"] != nil
      || environment["ANYPICK_MACOS_DESIGN"] == "1"
    isFixture = fixture
    if fixture {
      store = nil
      designModel = MacOSDesignModel.fromEnvironment(environment)
    } else {
      let created = TrayStore(
        initialCount: CommandLine.arguments.count > 1 ? Int(CommandLine.arguments[1]) ?? 0 : 0
      )
      store = created
      designModel = ProductionDesignModel(store: created)
    }
    super.init()
    Self.instance = self
  }

  /// Only the menu-bar Quit button should kill the process.
  static func requestQuit() {
    guard let instance else {
      NSApp.terminate(nil)
      return
    }
    instance.allowTerminate = true
    NSApp.terminate(nil)
  }

  func applicationDidFinishLaunching(_ notification: Notification) {
    AnyPickAppIcon.applyToApp()
    let serviceSmoke = ProcessInfo.processInfo.environment["ANYPICK_APP_SERVICE_SMOKE_PATH"] != nil
    if serviceSmoke {
      // The installed-app smoke exercises the real bundled helper without
      // entering AppKit's interactive window/menu tracking loops.
      NSApp.setActivationPolicy(.accessory)
      for window in NSApp.windows { window.orderOut(nil) }
    } else {
      NSApp.setActivationPolicy(.regular)
      NSApp.activate(ignoringOtherApps: true)
      retainMainWindows()
      NotificationCenter.default.addObserver(
        self,
        selector: #selector(windowDidBecomeKey(_:)),
        name: NSWindow.didBecomeKeyNotification,
        object: nil
      )
      DispatchQueue.main.async { [weak self] in self?.presentMainWindow() }
      DispatchQueue.main.asyncAfter(deadline: .now() + 0.15) { [weak self] in
        self?.presentMainWindow()
      }
    }

    if isFixture {
      if ProcessInfo.processInfo.environment["ANYPICK_MACOS_RENDER_PATH"] != nil {
        DispatchQueue.main.asyncAfter(deadline: .now() + 2.2) {
          self.captureIfRequested(attemptsRemaining: 60)
        }
      }
      return
    }

    startBundledService()
  }

  func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
    false
  }

  func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
    // Dock Quit / ⌘Q / File → Quit: leave the Dock, keep the menu-bar extra
    // and the helper. Only tray Quit sets `allowTerminate`.
    if !allowTerminate {
      withdrawToTray()
      return .terminateCancel
    }
    guard !isFixture, let serviceProcess else { return .terminateNow }
    if terminationPending { return .terminateLater }
    terminationPending = true
    Task {
      await serviceProcess.stop()
      await MainActor.run { sender.reply(toApplicationShouldTerminate: true) }
    }
    return .terminateLater
  }

  func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
    presentMainWindow()
    sender.activate(ignoringOtherApps: true)
    return true
  }

  /// Hide windows and drop the Dock tile. Menu-bar extra stays.
  private func withdrawToTray() {
    for window in NSApp.windows where !Self.isStatusItemWindow(window) {
      window.orderOut(nil)
    }
    NSApp.setActivationPolicy(.accessory)
  }

  @objc private func windowDidBecomeKey(_ notification: Notification) {
    guard let window = notification.object as? NSWindow else { return }
    retainIfMainWindow(window)
  }

  private func retainMainWindows() {
    for window in NSApp.windows { retainIfMainWindow(window) }
  }

  private func retainIfMainWindow(_ window: NSWindow) {
    guard isMainWindow(window) else { return }
    // Keep the SwiftUI Window alive after the close button so Dock / tray
    // can order it front instead of trying to recreate a torn-down scene.
    window.isReleasedWhenClosed = false
  }

  func presentMainWindow() {
    if NSApp.activationPolicy() != .regular {
      NSApp.setActivationPolicy(.regular)
    }
    AnyPickAppIcon.applyToApp()
    for window in NSApp.windows where !Self.isStatusItemWindow(window) {
      if window.title.isEmpty, window.frame.width > 180 {
        window.title = "AnyPick"
      }
    }
    let prefersTray = isFixture
      && ProcessInfo.processInfo.environment["ANYPICK_MACOS_FLOW"]?.hasPrefix("tray") == true
    let tray = NSApp.windows.first { $0.title == "Menu Bar" }
    let main = NSApp.windows.first(where: isMainWindow)
    if prefersTray {
      tray?.makeKeyAndOrderFront(nil)
    } else {
      main?.makeKeyAndOrderFront(nil)
    }
  }

  private func startBundledService() {
    guard let store else { return }
    let appVersion = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString")
      as? String ?? "(devel)"
    let service = GoServiceProcess(
      appVersion: appVersion,
      launchAtLogin: MacOSHostEffects.launchAtLoginEnabled,
      onFrame: { [weak store] data in store?.consume(data) },
      onState: { [weak store] state in store?.updateServiceState(state) }
    )
    serviceProcess = service
    store.attachCommandSink { [weak service] command in
      guard let service else { return }
      Task { await service.send(command) }
    }
    Task { await service.start() }
#if DEBUG
    runServiceSmokeIfRequested(store: store)
#endif
  }

#if DEBUG
  private func runServiceSmokeIfRequested(store: TrayStore) {
    guard let marker = ProcessInfo.processInfo.environment["ANYPICK_APP_SERVICE_SMOKE_PATH"],
          !marker.isEmpty else { return }
    Task { @MainActor in
      for _ in 0..<200 {
        if store.snapshotReady, store.serviceState == .ready {
          await serviceProcess?.stop()
          try? Data("ready\n".utf8).write(to: URL(fileURLWithPath: marker), options: .atomic)
          Darwin.exit(EXIT_SUCCESS)
        }
        if case .degraded(let reason) = store.serviceState {
          await serviceProcess?.stop()
          try? Data("degraded: \(reason)\n".utf8)
            .write(to: URL(fileURLWithPath: marker), options: .atomic)
          Darwin.exit(EXIT_FAILURE)
        }
        try? await Task.sleep(nanoseconds: 50_000_000)
      }
      await serviceProcess?.stop()
      try? Data("timeout\n".utf8).write(to: URL(fileURLWithPath: marker), options: .atomic)
      Darwin.exit(EXIT_FAILURE)
    }
  }
#endif

  private func isMainWindow(_ window: NSWindow) -> Bool {
    if Self.isStatusItemWindow(window) { return false }
    if window.identifier?.rawValue == "main" { return true }
    return window.title == "AnyPick" && window.canBecomeMain && window.frame.width > 500
  }

  private static func isStatusItemWindow(_ window: NSWindow) -> Bool {
    if window.title == "Item-0" { return true }
    let name = String(describing: type(of: window))
    return name.contains("StatusBar") || name.contains("MenuBar") || name.contains("StatusItem")
  }

  private func captureIfRequested(attemptsRemaining: Int) {
    guard let renderPath = ProcessInfo.processInfo.environment["ANYPICK_MACOS_RENDER_PATH"] else {
      return
    }
    presentMainWindow()
    let visible = NSApp.windows.filter {
      $0.contentView != nil && $0.frame.width > 80 && $0.frame.height > 40
    }
    let prefersTray = ProcessInfo.processInfo.environment["ANYPICK_MACOS_FLOW"]?.hasPrefix("tray")
      == true
    let tray = visible.first { $0.title == "Menu Bar" }
    let main = visible.first(where: isMainWindow) ?? visible.first
    let window = prefersTray ? (tray ?? main) : (main?.attachedSheet ?? main)
    guard let window, let contentView = window.contentView, !contentView.bounds.isEmpty else {
      retryCapture(renderPath: renderPath, attemptsRemaining: attemptsRemaining)
      return
    }
    contentView.layoutSubtreeIfNeeded()
    window.displayIfNeeded()
    // CGWindowListCreateImage is obsoleted on macOS 15+ SDKs; render the view
    // hierarchy off-screen instead (own window only, no capture permission).
    let bounds = contentView.bounds
    guard let representation = contentView.bitmapImageRepForCachingDisplay(in: bounds) else {
      retryCapture(renderPath: renderPath, attemptsRemaining: attemptsRemaining)
      return
    }
    contentView.cacheDisplay(in: bounds, to: representation)
    if let data = representation.representation(using: .png, properties: [:]) {
      try? data.write(to: URL(fileURLWithPath: renderPath), options: .atomic)
    }
    exit(EXIT_SUCCESS)
  }

  private func retryCapture(renderPath: String, attemptsRemaining: Int) {
    if attemptsRemaining > 1 {
      DispatchQueue.main.asyncAfter(deadline: .now() + 0.45) {
        self.captureIfRequested(attemptsRemaining: attemptsRemaining - 1)
      }
      return
    }
    fputs("[macOS] Could not capture a visible window.\n", stderr)
    exit(EXIT_SUCCESS)
  }
}
