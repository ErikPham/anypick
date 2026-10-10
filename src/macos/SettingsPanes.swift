import SwiftUI

struct SettingsGeneralPane: View {
  @ObservedObject var model: MacOSDesignModel

  @ViewBuilder
  var body: some View {
    Section {
      Toggle("Open at Login", isOn: $model.openAtLogin)
        .help("Launch AnyPick automatically when you sign in to this Mac.")
        .disabled(model.isBusy)
        .accessibilityIdentifier("settings.open-at-login")

      Toggle(
        "Start Enabled Proxies at Launch",
        isOn: Binding(
          get: { model.startProxiesAtLaunch },
          set: model.setStartProxiesAtLaunch
        )
      )
      .help("Start enabled proxies on launch. Apps can start others as needed.")
      .disabled(model.isBusy)
      .accessibilityIdentifier("settings.start-enabled-proxies")
    } header: {
      Text("Startup")
    }

    Section {
      LabeledContent("Setup Checklist") {
        Button("Show", action: model.showGettingStartedAgain)
          .accessibilityIdentifier("settings.show-getting-started")
      }
    } header: {
      Text("Getting Started")
    } footer: {
      Text("Show the setup checklist on Overview again.")
    }
  }
}

struct SettingsUsagePane: View {
  @ObservedObject var model: MacOSDesignModel

  @ViewBuilder
  var body: some View {
    Section {
      Toggle("Show Usage Remaining", isOn: $model.showUsage)
        .help("Show usage reported for the account active in its provider app.")
        .disabled(model.isBusy)
        .accessibilityIdentifier("settings.show-usage")

      Toggle(
        "Skip Accounts That Reach Their Usage Limit",
        isOn: Binding(
          get: { model.warnBeforeLowUsage },
          set: model.setWarnBeforeLowUsage
        )
      )
      .disabled(model.isBusy)
      .help("A confirmed limit cools that account down in proxy pools.")
      .accessibilityIdentifier("settings.quota-guard")
    } header: {
      Text("Usage")
    }
  }
}
