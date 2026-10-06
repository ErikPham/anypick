import SwiftUI

private enum DesignSettingsPane: String, CaseIterable, Identifiable {
  case general = "General"
  case usage = "Usage"
  case localProxies = "Proxies"
  case security = "Security"
  case diagnostics = "Diagnostics"

  var id: String { rawValue }

  var symbol: String {
    switch self {
    case .general: "gearshape.fill"
    case .usage: "chart.bar"
    case .localProxies: "server.rack"
    case .security: "lock.fill"
    case .diagnostics: "stethoscope"
    }
  }

}

struct SettingsView: View {
  @ObservedObject var model: MacOSDesignModel
  @AppStorage("dev.anypick.settings.selectedPane") private var pane: DesignSettingsPane = .general

  var body: some View {
    NavigationSplitView {
      List(selection: $pane) {
        ForEach(DesignSettingsPane.allCases) { item in
          Label {
            Text(item.rawValue)
          } icon: {
            Image(systemName: item.symbol)
              .symbolRenderingMode(.hierarchical)
          }
          .tag(item)
        }
      }
      .listStyle(.sidebar)
      .navigationSplitViewColumnWidth(min: 180, ideal: 200, max: 240)
      .accessibilityIdentifier("settings.tabs")
    } detail: {
      Group {
        switch pane {
        case .general: SettingsGeneralPane(model: model)
        case .usage: SettingsUsagePane(model: model)
        case .localProxies: SettingsLocalProxiesPane(model: model)
        case .security: SettingsSecurityPane(model: model)
        case .diagnostics: SettingsDiagnosticsPane(model: model)
        }
      }
      .navigationTitle(pane.rawValue)
    }
    .frame(minWidth: 700, minHeight: 460)
    .followingDesignAppearance(model.appearance)
    .accessibilityElement(children: .contain)
    .accessibilityIdentifier("settings.tabs")
  }
}
