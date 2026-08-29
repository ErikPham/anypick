import AppKit
import SwiftUI

struct AnyPickMenuBarScene: Scene {
  @ObservedObject var model: MacOSDesignModel

  var body: some Scene {
    MenuBarExtra {
      MenuBarMenu(model: model)
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

  var body: some Commands {
    CommandGroup(after: .newItem) {
      Button("Add Account…") { open(.accounts) }
        .keyboardShortcut("n", modifiers: .command)
    }
    CommandGroup(replacing: .undoRedo) {
      Button("Undo") { model.performUndo() }
        .keyboardShortcut("z", modifiers: .command)
        .disabled(!model.canUndo)
    }
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
    }
  }

  private func open(_ destination: DesignDestination) {
    model.destination = destination
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
