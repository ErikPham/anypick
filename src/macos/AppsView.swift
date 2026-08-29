import SwiftUI

struct AppsView: View {
  @ObservedObject var model: MacOSDesignModel

  var body: some View {
    Group {
      if model.apps.isEmpty {
        DesignEmptyState(
          title: "No Apps",
          message: "Install a supported coding app, then refresh.",
          symbol: "app.badge",
          actionTitle: "Refresh",
          action: model.refresh
        )
      } else {
        DesignGroupedPane {
          Section {
            ForEach(model.apps) { app in
              HStack(spacing: 10) {
                Toggle(isOn: quickPanelBinding(app)) {}
                  .toggleStyle(.checkbox)
                  .labelsHidden()
                  .accessibilityLabel("Show \(app.name) in Quick Panel")
                  .disabled(model.isBusy)
                NavigationLink(value: DesignRouteID.app(app.id)) {
                  DesignIdentityRow(
                    title: app.name,
                    detail: appSubtitle(app),
                    issue: app.issue
                  ) {
                    DesignProviderIcon(provider: app.id, fallbackSymbol: app.symbol)
                  }
                }
                .accessibilityIdentifier("apps.row.\(app.id)")
              }
            }
          } footer: {
            Text("Check an app to show it in the Quick Panel. Select its row to change how it connects.")
          }
        }
      }
    }
    .accessibilityIdentifier("apps.list")
  }

  private func quickPanelBinding(_ app: DesignApp) -> Binding<Bool> {
    Binding(
      get: { model.apps.first(where: { $0.id == app.id })?.showInQuickPanel ?? true },
      set: { model.setQuickPanelShown(appID: app.id, shows: $0) }
    )
  }

  private func appSubtitle(_ app: DesignApp) -> String {
    let model = appModel(app)
    if model.isEmpty || app.detail.localizedCaseInsensitiveContains(model) {
      return app.detail
    }
    if app.detail.isEmpty { return model }
    return "\(app.detail) · \(model)"
  }

  private func appModel(_ app: DesignApp) -> String {
    if app.selectedRouteID.hasPrefix("native-") { return "Native Login" }
    if let modelID = app.modelRoles.first(where: { $0.id == "default" && !$0.modelID.isEmpty })?
      .modelID
    {
      return modelID
    }
    if app.reasoningModel == "Native Login" { return "Native Login" }
    return ""
  }
}
