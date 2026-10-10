import AppKit
import SwiftUI

private enum AppVersion {
  static var marketing: String {
    let value = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String
    return value.flatMap { $0.isEmpty ? nil : $0 } ?? "(devel)"
  }
}

struct AnyPickMenuBarScene: Scene {
  @ObservedObject var model: MacOSDesignModel
  @Environment(\.openWindow) private var openWindow

  var body: some Scene {
    // A main-window render stays out of the menu bar too.
    MenuBarExtra(isInserted: .constant(!AnyPickAppDelegate.rendersOffscreen)) {
      MenuBarMenu(model: model)
        .onAppear {
          AnyPickAppDelegate.openMainWindowAction = {
            openWindow(id: "main")
          }
        }
    } label: {
      AnyPickMenuBarLabel(model: model)
    }
    .menuBarExtraStyle(.window)
  }
}

struct AnyPickMenuBarLabel: View {
  @ObservedObject var model: MacOSDesignModel

  private var attentionCount: Int { trayAttentionItems(from: model).count }

  var body: some View {
    ZStack(alignment: .topTrailing) {
      Image(nsImage: AnyPickAppIcon.menuBarImage())
      if attentionCount > 0 {
        Circle()
          .fill(Color.orange)
          .frame(width: 6, height: 6)
          .overlay(
            Circle()
              .strokeBorder(Color.primary.opacity(0.35), lineWidth: 0.5)
          )
          .offset(x: 1, y: -1)
          .accessibilityHidden(true)
      }
    }
    .accessibilityLabel(
      attentionCount > 0
        ? "AnyPick, \(attentionCount) \(attentionCount == 1 ? "item needs" : "items need") attention"
        : "AnyPick"
    )
    .accessibilityHint(attentionCount > 0 ? "Open AnyPick to review routing" : "")
  }
}

struct AppCommands: Commands {
  @ObservedObject var model: MacOSDesignModel
  @Environment(\.openWindow) private var openWindow

  var body: some Commands {
    // The standard panel appends CFBundleVersion as "(1)"; an empty `.version`
    // keeps the panel focused on the marketing version.
    CommandGroup(replacing: .appInfo) {
      Button("About AnyPick") {
        NSApp.orderFrontStandardAboutPanel(options: [
          .applicationVersion: AppVersion.marketing,
          .version: "",
        ])
        NSApp.activate(ignoringOtherApps: true)
      }
      .help("View the app version and product information.")
    }
    CommandGroup(after: .appInfo) {
      Button("Settings…") { open(.settings) }
        .keyboardShortcut(",", modifiers: .command)
        .help("Open Settings in the main window.")
    }
    CommandGroup(after: .newItem) {
      Button("Add Login…") {
        model.requestAddLogin()
        open(.accounts)
      }
        .keyboardShortcut("n", modifiers: .command)
        .help("Save a login or API key for an AI provider.")
    }
    // No Edit → Undo: mutations apply server-side with no compensation
    // path, so a local undo would be reverted by the next snapshot.
    CommandGroup(replacing: .undoRedo) {}
    CommandGroup(after: .toolbar) {
      Button("Refresh") { model.refresh() }
        .keyboardShortcut("r", modifiers: .command)
        .help("Refresh accounts, apps, services, and usage.")
      Button("Restart Proxies") { model.restartRunningProxies() }
        .keyboardShortcut("r", modifiers: [.command, .shift])
        .help("Restart every running proxy service.")
    }
    CommandMenu("Go") {
      ForEach(Array(DesignDestination.allCases.enumerated()), id: \.element.id) {
        index, destination in
        Button(destination.title) { open(destination) }
          .help("Open \(destination.title).")
          .keyboardShortcut(
            KeyEquivalent(Character("\(index + 1)")),
            modifiers: .command
          )
      }
    }
  }

  private func open(_ destination: DesignDestination) {
    model.destination = destination
    NSApp.setActivationPolicy(.regular)
    openWindow(id: "main")
    NSApp.activate(ignoringOtherApps: true)
  }
}

@main
struct AnyPickApp: App {
  @NSApplicationDelegateAdaptor(AnyPickAppDelegate.self) private var delegate

  var body: some Scene {
    Window("AnyPick", id: "main") {
      MainWindow(model: delegate.designModel)
        .followingDesignAppearance(delegate.designModel.appearance)
    }
    .defaultSize(width: 920, height: 640)
    .windowResizability(.automatic)
    .commands {
      AppCommands(model: delegate.designModel)
      SidebarCommands()
    }

#if DEBUG
    Window("Menu Bar", id: "tray") {
      MenuBarPreview(model: delegate.designModel)
    }
    .defaultSize(width: 360, height: 480)
    .windowStyle(.hiddenTitleBar)
    .windowResizability(.contentSize)
#endif

    AnyPickMenuBarScene(model: delegate.designModel)
  }
}
