import SwiftUI

struct AppsView: View {
  @ObservedObject var model: MacOSDesignModel
  @State private var searchQuery = ""
  @State private var showingAddApp = false

  private var filteredApps: [DesignApp] {
    let query = searchQuery.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !query.isEmpty else { return model.apps }
    return model.apps.filter {
      $0.name.localizedCaseInsensitiveContains(query)
        || $0.detail.localizedCaseInsensitiveContains(query)
    }
  }

  var body: some View {
    Group {
      if model.apps.isEmpty {
        ContentUnavailableView {
          Label("No Apps Configured", systemImage: "square.grid.2x2")
        } description: {
          Text("Add the coding apps you want to configure with AnyPick accounts and proxies.")
        } actions: {
          Button("Add App…") {
            showingAddApp = true
          }
          .buttonStyle(.borderedProminent)
          .accessibilityIdentifier("apps.empty.add")
        }
      } else {
        DesignGroupedPane(actions: { paneActions }) {
          Section {
            ForEach(filteredApps) { app in
              appRow(app)
            }
          } header: {
            Text(
              searchQuery.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
                ? "Apps (\(filteredApps.count))"
                : "Matching Apps (\(filteredApps.count))")
          }
        }
      }
    }
    .toolbar {
      ToolbarItem(placement: .primaryAction) {
        DesignToolbarSearch(text: $searchQuery, prompt: "Search Apps")
          .frame(minWidth: 140, idealWidth: 160, maxWidth: 200)
      }
    }
    .sheet(isPresented: $showingAddApp) {
      AddAppSheet(model: model)
    }
    .accessibilityIdentifier("apps.list")
  }

  private var paneActions: some View {
    DesignPaneActions {
      DesignSheetButton(
        title: "Add App…",
        enabled: !model.isBusy,
        identifier: "apps.add"
      ) { showingAddApp = true }
    }
  }

  private func appRow(_ app: DesignApp) -> some View {
    let isManaged = isAppManagedByAnyPick(app)
    return NavigationLink(value: DesignRouteID.app(app.id)) {
      DesignIdentityRow(
        title: app.name,
        detail: appSubtitle(app),
        issue: app.issue
      ) {
        DesignProviderIcon(
          provider: app.id,
          fallbackSymbol: app.symbol,
          size: 26
        )
      } trailing: {
        if isManaged {
          DesignStatusLabel(status: app.status)
        } else {
          if !isAppRouteConfigured(app) && app.status != .ready {
            HStack(spacing: 6) {
              Circle()
                .fill(Color.orange)
                .frame(width: 7, height: 7)
              Text("Set Up…")
                .font(.subheadline)
                .foregroundStyle(.secondary)
            }
          } else {
            Text("Direct")
              .font(.subheadline)
              .foregroundStyle(.secondary)
          }
        }
      }
    }
    .accessibilityIdentifier("apps.row.\(app.id)")
    .contextMenu {
      Button(role: .destructive) {
        model.removeApp(appID: app.id)
      } label: {
        Label("Remove App", systemImage: "trash")
      }
      .disabled(model.isBusy)
      if isAppManagedByAnyPick(app) {
        Button(role: .destructive) {
          model.resetAppConfiguration(appID: app.id)
        } label: {
          Label("Disconnect from AnyPick", systemImage: "bolt.slash")
        }
        .disabled(model.isBusy)
      }
      Divider()
      Toggle("Show in Quick Panel", isOn: quickPanelBinding(app))
        .disabled(model.isBusy)
    }
  }

  private func quickPanelBinding(_ app: DesignApp) -> Binding<Bool> {
    Binding(
      get: { model.apps.first(where: { $0.id == app.id })?.showInQuickPanel ?? true },
      set: { model.setQuickPanelShown(appID: app.id, shows: $0) }
    )
  }

  private func appSubtitle(_ app: DesignApp) -> String {
    let route = app.routeOptions.first(where: { $0.id == app.selectedRouteID })
    let isManaged = isAppManagedByAnyPick(app)

    if isManaged {
      let routeName = route?.name ?? (app.detail.isEmpty ? "Connected" : app.detail)
      var parts: [String] = [routeName]

      if let detail = route?.detail, !detail.isEmpty,
        detail != routeName,
        !detail.hasPrefix("Direct"), !detail.hasPrefix("Managed"), !detail.hasPrefix("Login managed")
      {
        parts.append(detail)
      } else if app.detail.hasPrefix("Uses ") {
        let clean = String(app.detail.dropFirst("Uses ".count))
        for sub in clean.components(separatedBy: " · ") {
          if !parts.contains(sub) && !sub.isEmpty {
            parts.append(sub)
          }
        }
      }

      let model = appModel(app)
      if !model.isEmpty && model != "Direct" && !parts.contains(model) {
        parts.append(model)
      }
      return parts.joined(separator: " · ")
    } else {
      if let route = route {
        if route.name == "Direct account" {
          if !route.detail.isEmpty, !route.detail.hasPrefix("Login managed"),
            !route.detail.hasPrefix("Direct")
          {
            return route.detail
          }
        } else {
          var label = route.name
          if !route.detail.isEmpty, !route.detail.hasPrefix("Login managed"),
            !route.detail.hasPrefix("Direct"), route.detail != route.name
          {
            label = "\(label) · \(route.detail)"
          }
          return label
        }
      }

      if app.detail.hasPrefix("Signed in as ") {
        return app.detail.replacingOccurrences(of: "Signed in as ", with: "")
      }
      if app.detail.hasPrefix("Uses ") {
        return app.detail.replacingOccurrences(of: "Uses ", with: "")
      }
      if app.detail == "Current login" {
        return "Current login"
      }
      if !app.detail.isEmpty && app.detail != "No usable connection"
        && app.detail != "No account selected"
      {
        return app.detail
      }
      if isAppRouteConfigured(app) || app.status == .ready {
        return "Current login"
      }
      return "Not configured"
    }
  }

  private func appModel(_ app: DesignApp) -> String {
    if app.selectedRouteID.hasPrefix("native-") { return friendlyRouteKindLabel(.native) }
    if let modelID = app.modelRoles.first(where: { $0.id == "default" && !$0.modelID.isEmpty })?
      .modelID
    {
      return cleanModelDisplayName(modelID)
    }
    if app.reasoningModel == "Direct account" { return friendlyRouteKindLabel(.native) }
    return ""
  }
}
