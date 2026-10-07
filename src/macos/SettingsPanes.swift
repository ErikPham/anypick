import AppKit
import SwiftUI

struct SettingsGeneralPane: View {
  @ObservedObject var model: MacOSDesignModel
  @Environment(\.openWindow) private var openWindow

  var body: some View {
    Form {
      Section {
        Toggle("Open at Login", isOn: $model.openAtLogin)
          .disabled(model.isBusy)
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
          Text(AppVersion.label)
            .foregroundStyle(.secondary)
            .textSelection(.enabled)
            .accessibilityIdentifier("settings.version")
        } label: {
          Label {
            Text("AnyPick")
          } icon: {
            DesignProductMark()
          }
        }
      } header: {
        Text("About")
      } footer: {
        Text("Pick any. Code on.")
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
          .disabled(model.isBusy)
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

struct SettingsDiagnosticsPane: View {
  @ObservedObject var model: MacOSDesignModel
  @Environment(\.openWindow) private var openWindow

  private var missingCodexSources: [DesignCodexProfileSource] {
    let profiled = Set(model.codexProfiles.map(\.sourceID))
    return model.codexProfileSources.filter {
      $0.kind == "account" && !profiled.contains($0.sourceID)
    }
  }

  private var creatableCodexSources: [DesignCodexProfileSource] {
    missingCodexSources.filter { !$0.models.isEmpty }
  }

  private var unreadyCodexSources: [DesignCodexProfileSource] {
    missingCodexSources.filter { $0.models.isEmpty }
  }

  var body: some View {
    Form {
      Section {
        LabeledContent("Status") {
          if let report = model.doctorReport {
            Label(report.status.capitalized, systemImage: statusSymbol(report.status))
              .foregroundStyle(statusColor(report.status))
          } else if model.doctorIsRunning {
            ProgressView("Checking")
              .controlSize(.small)
          } else {
            Text("Not checked")
              .foregroundStyle(.secondary)
          }
        }

        if let error = model.doctorError {
          Text(error)
            .foregroundStyle(.secondary)
        }

        if let report = model.doctorReport {
          ForEach(report.findings) { finding in
            LabeledContent(finding.check.replacingOccurrences(of: "-", with: " ").capitalized) {
              VStack(alignment: .trailing, spacing: 2) {
                Text(finding.status.capitalized)
                  .foregroundStyle(statusColor(finding.status))
                if let detail = finding.detail, !detail.isEmpty {
                  Text(detail)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.trailing)
                    .textSelection(.enabled)
                }
              }
            }
          }
        }

        Button(model.doctorIsRunning ? "Running Doctor…" : "Run Doctor") {
          model.runDoctor()
        }
        .disabled(model.doctorIsRunning || model.isBusy)
        .accessibilityIdentifier("settings.run-doctor")
      } header: {
        Text("Installation Health")
      } footer: {
        Text("Doctor checks saved account references, proxies, routing, plugins, and proxy environment settings. It does not change data.")
      }

      Section {
        if missingCodexSources.isEmpty {
          LabeledContent("Profiles") {
            Text("All compatible accounts have a profile.")
              .foregroundStyle(.secondary)
          }
        } else {
          LabeledContent("Missing Profiles", value: "\(missingCodexSources.count)")
          ForEach(unreadyCodexSources) { source in
            LabeledContent {
              Button("Refresh Models") { refreshModels(for: source) }
                .disabled(model.isBusy)
            } label: {
              VStack(alignment: .leading, spacing: 2) {
                Text(source.label)
                Text("Model list unavailable")
                  .font(.caption)
                  .foregroundStyle(.secondary)
              }
            }
          }
          Button("Create Missing Profiles…") {
            _ = model.createMissingCodexProfiles(
              sourceIDs: creatableCodexSources.map(\.sourceID)
            )
          }
          .disabled(model.isBusy || creatableCodexSources.isEmpty)
          .accessibilityIdentifier("settings.create-codex-profiles")
        }

        if !unreadyCodexSources.isEmpty {
          Button("Open Accounts & APIs…") { open(.accounts) }
            .disabled(model.isBusy)
        }
      } header: {
        Text("Codex Profiles")
      } footer: {
        Text("Profiles can be created when their account model list is available. Refresh accounts above, then create the remaining profiles here.")
      }
    }
    .formStyle(.grouped)
  }

  private func refreshModels(for source: DesignCodexProfileSource) {
    guard source.sourceID.hasPrefix("account/") else { return }
    let accountID = String(source.sourceID.dropFirst("account/".count))
    model.refreshAccount(id: accountID)
  }

  private func open(_ destination: DesignDestination) {
    model.destination = destination
    openWindow(id: "main")
    NSApp.activate(ignoringOtherApps: true)
  }

  private func statusSymbol(_ status: String) -> String {
    switch status {
    case "ok": "checkmark.circle.fill"
    case "degraded": "exclamationmark.triangle.fill"
    default: "xmark.octagon.fill"
    }
  }

  private func statusColor(_ status: String) -> Color {
    switch status {
    case "ok": .green
    case "degraded", "warn": .orange
    default: .red
    }
  }
}

/// The one version string every About surface shows: General → About and
/// AnyPick → About AnyPick both read `CFBundleShortVersionString`, which
/// `set-version` writes as MARKETING_VERSION. The build number stays out so
/// the two never disagree.
enum AppVersion {
  static var marketing: String {
    let value = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String
    return value.flatMap { $0.isEmpty ? nil : $0 } ?? "(devel)"
  }

  static var label: String { "Version \(marketing)" }
}
