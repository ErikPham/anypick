import AppKit
import SwiftUI

struct SettingsGeneralPane: View {
  @ObservedObject var model: MacOSDesignModel
  @Environment(\.openWindow) private var openWindow

  var body: some View {
    Form {
      Section {
        Toggle("Open at Login", isOn: $model.openAtLogin)
      } header: {
        Text("Startup")
      }

      Section {
        LabeledContent("Getting Started") {
          Button("Show") {
            model.showGettingStartedAgain()
            open(.overview)
          }
            .disabled(model.shouldShowOnboarding && model.destination == .overview)
            .accessibilityIdentifier("settings.getting-started")
        }
      } footer: {
        Text("Shown on Overview until setup is finished.")
      }

      Section {
        LabeledContent {
          Text("Pick any. Code on.")
            .foregroundStyle(.secondary)
        } label: {
          Label {
            Text("AnyPick")
          } icon: {
            DesignProductMark()
          }
        }
      } header: {
        Text("About")
      }
    }
    .formStyle(.grouped)
    .onChange(of: model.openAtLogin) { _, value in model.setOpenAtLogin(value) }
  }

  private func open(_ destination: DesignDestination) {
    model.destination = destination
    openWindow(id: "main")
    NSApp.activate(ignoringOtherApps: true)
  }
}

struct SettingsUsagePane: View {
  @ObservedObject var model: MacOSDesignModel

  var body: some View {
    Form {
      Section {
        Toggle("Show Usage Remaining", isOn: $model.showUsage)
      } header: {
        Text("Display")
      } footer: {
        Text("From the login currently signed in on this Mac.")
      }

      Section {
        Toggle(
          "Quota Guard",
          isOn: Binding(
            get: { model.warnBeforeLowUsage },
            set: model.setWarnBeforeLowUsage
          )
        )
        .disabled(model.isBusy)
        .accessibilityIdentifier("settings.quota-guard")
      } header: {
        Text("Usage Alerts")
      } footer: {
        Text("Quota Guard pauses a pooled proxy account after a confirmed usage limit. Native app logins are never switched automatically.")
      }
    }
    .formStyle(.grouped)
    .onChange(of: model.showUsage) { _, value in model.setShowUsage(value) }
  }
}

struct SettingsLocalProxiesPane: View {
  @ObservedObject var model: MacOSDesignModel
  @Environment(\.openWindow) private var openWindow

  var body: some View {
    Form {
      Section {
        Toggle(
          "Start Enabled Proxies at Launch",
          isOn: Binding(
            get: { model.startProxiesAtLaunch },
            set: model.setStartProxiesAtLaunch
          )
        )
        .disabled(model.isBusy)
        LabeledContent("Proxies") {
          Button("Open Proxies…") { open(.proxies) }
            .accessibilityIdentifier("settings.open-proxies")
        }
      } header: {
        Text("Startup")
      } footer: {
        Text("A proxy can still start when an app needs it.")
      }
    }
    .formStyle(.grouped)
  }

  private func open(_ destination: DesignDestination) {
    model.destination = destination
    openWindow(id: "main")
    NSApp.activate(ignoringOtherApps: true)
  }
}

struct SettingsSecurityPane: View {
  @ObservedObject var model: MacOSDesignModel
  @Environment(\.openWindow) private var openWindow

  var body: some View {
    Form {
      Section {
        LabeledContent("Hide Tokens") {
          Text("On")
            .foregroundStyle(.secondary)
        }
      } header: {
        Text("Data")
      } footer: {
        Text("API keys and proxy tokens never appear in the menu, activity, or logs.")
      }

      Section {
        LabeledContent("Local Access Only") {
          Text("On")
            .foregroundStyle(.secondary)
        }
      } header: {
        Text("Network")
      } footer: {
        Text("Proxies accept connections only from this Mac.")
      }

      Section {
        LabeledContent("Account Backups") {
          Text("Unencrypted")
            .foregroundStyle(.secondary)
        }
        Button("Open Account Backups…") { open(.backups) }
          .accessibilityIdentifier("settings.review-backups")
      } header: {
        Text("Backups")
      } footer: {
        Text("Backup files include credentials.")
      }
    }
    .formStyle(.grouped)
  }

  private func open(_ destination: DesignDestination) {
    model.destination = destination
    openWindow(id: "main")
    NSApp.activate(ignoringOtherApps: true)
  }
}
