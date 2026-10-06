import AppKit
import SwiftUI

struct AnyPickMenuBarScene: Scene {
  @ObservedObject var model: MacOSDesignModel
  @Environment(\.openWindow) private var openWindow

  var body: some Scene {
    MenuBarExtra {
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
      Image(systemName: "point.3.connected.trianglepath.dotted")
        .symbolRenderingMode(.monochrome)
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
  @Environment(\.openSettings) private var openSettings

  var body: some Commands {
    CommandGroup(after: .newItem) {
      Button("Add Login…") {
        model.requestAddLogin()
        open(.accounts)
      }
        .keyboardShortcut("n", modifiers: .command)
    }
    // No Edit → Undo: mutations apply server-side with no compensation
    // path, so a local undo would be reverted by the next snapshot.
    CommandGroup(replacing: .undoRedo) {}
    CommandGroup(after: .toolbar) {
      Button("Refresh") { model.refresh() }
        .keyboardShortcut("r", modifiers: .command)
      Button("Restart Proxies") { model.restartRunningProxies() }
        .keyboardShortcut("r", modifiers: [.command, .shift])
    }
    CommandMenu("Go") {
      ForEach(Array(DesignDestination.allCases.enumerated()), id: \.element.id) {
        index, destination in
        Button(destination.title) { open(destination) }
          .keyboardShortcut(
            KeyEquivalent(Character("\(index + 1)")),
            modifiers: .command
          )
      }
      Divider()
      Button("Settings…") { openSettings() }
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

    Settings {
      SettingsView(model: delegate.designModel)
        .followingDesignAppearance(delegate.designModel.appearance)
    }
  }
}
