import SwiftUI

struct FirstRunWelcomeSheet: View {
  @ObservedObject var model: MacOSDesignModel

  var body: some View {
    DesignSheetHost(
      title: "Welcome to AnyPick",
      width: 480,
      height: 390,
      primaryTitle: "Continue",
      showsCancel: false,
      onCancel: model.dismissFirstRunWelcome,
      onPrimary: model.dismissFirstRunWelcome
    ) {
      Form {
        Section {
          Text("Connect your coding tools with a provider account or API gateway. Credentials and local services stay on this Mac.")
            .foregroundStyle(.secondary)
        }

        Section {
          welcomeRow(
            title: "Local by default",
            detail: "Credentials stay on this Mac",
            symbol: "lock.macwindow"
          )
          welcomeRow(
            title: "Flexible connections",
            detail: "Provider accounts and API gateways",
            symbol: "arrow.triangle.branch"
          )
          welcomeRow(
            title: "Only what you need",
            detail: "Starts account proxies only when required",
            symbol: "server.rack"
          )
        } footer: {
          Text("You can add or change connections at any time.")
        }
      }
    }
    .interactiveDismissDisabled()
  }

  private func welcomeRow(title: String, detail: String, symbol: String) -> some View {
    Label {
      VStack(alignment: .leading, spacing: 2) {
        Text(title)
        Text(detail)
          .foregroundStyle(.secondary)
      }
    } icon: {
      Image(systemName: symbol)
    }
  }
}

struct OverviewView: View {
  @ObservedObject var model: MacOSDesignModel

  var body: some View {
    DesignGroupedPane {
      if !model.degradedConditions.isEmpty {
        Section("Needs Attention") {
          ForEach(model.degradedConditions) { condition in
            Label {
              VStack(alignment: .leading, spacing: 2) {
                Text(condition.title)
                Text(condition.detail)
                  .foregroundStyle(.secondary)
              }
            } icon: {
              Image(systemName: "exclamationmark.triangle.fill")
                .foregroundStyle(.orange)
            }
          }
        }
        .accessibilityIdentifier("overview.degraded-conditions")
      }

      if let attention = primaryAttention {
        Section {
          LabeledContent {
            Button(attention.actionTitle, action: attention.action)
          } label: {
            Label {
              VStack(alignment: .leading, spacing: 2) {
                Text(attention.title)
                Text(attention.message)
                  .foregroundStyle(.secondary)
              }
            } icon: {
              DesignWarningSymbol(scale: .medium)
            }
          }
        }
        .accessibilityIdentifier("overview.review-attention")
      }

      if model.shouldShowOnboarding {
        Section {
          setupRow(
            done: model.hasSavedLogin,
            title: "Save a Login",
            detail: model.hasSavedLogin
              ? "A login is saved on this Mac."
              : "Use a login already on this Mac.",
            actionTitle: "Save…",
            identifier: "overview.onboarding.save-login"
          ) {
            model.destination = .accounts
          }
          setupRow(
            done: model.hasInstalledApp,
            title: "Use an App",
            detail: model.hasInstalledApp
              ? "Claude Code, Codex, or another supported app is available."
              : "Install Claude Code or Codex, then refresh.",
            actionTitle: "Refresh",
            identifier: "overview.onboarding.use-app"
          ) {
            model.refresh()
          }
          setupRow(
            done: model.hasChosenRoute,
            title: "Point an App",
            detail: model.hasChosenRoute
              ? "An app is using a saved login."
              : "Point Claude or Codex at a saved login.",
            actionTitle: "Choose…",
            identifier: "overview.onboarding.choose-route"
          ) {
            model.destination = .apps
          }
          setupRow(
            done: model.isHubReady,
            title: "Set Up Proxy Hub (Optional)",
            detail: model.isHubReady
              ? "Hub sources are ready."
              : "One local endpoint for many models. Skip it if unsure.",
            actionTitle: "Set Up…",
            identifier: "overview.onboarding.proxy-hub"
          ) {
            model.destination = .proxyHub
          }
          Button("Hide") { model.dismissOnboarding() }
            .accessibilityIdentifier("overview.hide-onboarding")
        } header: {
          Text("Get Started")
        } footer: {
          Text(onboardingFooter)
        }
        .accessibilityIdentifier("overview.get-started")
      }

      // An empty Apps header above the checklist is pure noise on first run —
      // the "Use an App" step already covers it. Drop the section instead of
      // hiding it so no blank space remains.
      if !(model.apps.isEmpty && model.shouldShowOnboarding) {
        Section {
          ForEach(model.apps) { app in
            NavigationLink(value: DesignRouteID.app(app.id)) {
              DesignIdentityRow(
                title: app.name,
                detail: connectionSummary(app),
                issue: appIssue(app)
              ) {
                DesignProviderIcon(provider: app.id, fallbackSymbol: app.symbol)
              } trailing: {
                Text(transportLabel(app))
                  .foregroundStyle(isAppRouteConfigured(app) ? Color.secondary : Color.orange)
              }
            }
            .accessibilityIdentifier("overview.app.\(app.id)")
            .accessibilityLabel("\(app.name), \(connectionSummary(app)), \(transportLabel(app))")
          }
        } header: {
          Text("Apps")
        } footer: {
          if model.apps.contains(where: { !isAppRouteConfigured($0) }) {
            Text("Choose a login for each app before it can run.")
          }
        }
      }

      Section {
        DesignIdentityRow(
          title: "Proxies",
          detail: servicesSummary
        ) {
          Image(systemName: DesignDestination.proxies.symbol)
            .foregroundStyle(.secondary)
        } trailing: {
          Text(serviceStatusText)
            .foregroundStyle(.secondary)
            .monospacedDigit()
        }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("overview.services-summary")
      } header: {
        Text("Services")
      }

      // While the checklist is up, an empty "Events will appear here"
      // placeholder competes for attention without information. Show the
      // section once there is something to show or onboarding is done.
      if !model.activity.isEmpty || !model.shouldShowOnboarding {
        Section {
          if model.activity.isEmpty {
            Text("Events will appear here.")
              .foregroundStyle(.secondary)
          } else {
            ForEach(model.activity.prefix(3)) { item in
              HStack(alignment: .center, spacing: 10) {
                Image(
                  systemName: item.status == .ready || item.status == .running
                    ? item.symbol : item.status.symbol
                )
                .foregroundStyle(item.status.color)
                .frame(width: 16)
                .accessibilityLabel(item.status.rawValue)
                Text(item.title)
                Spacer(minLength: 8)
                Text(item.time)
                  .foregroundStyle(.secondary)
                  .monospacedDigit()
              }
              .accessibilityElement(children: .combine)
              .accessibilityIdentifier("overview.activity.\(item.id)")
            }
          }
        } header: {
          Text("Recent Activity")
        }
      }
    }
    .onChange(of: model.isOnboardingCoreComplete) { _, done in
      // Dismiss as soon as the three core steps are done, however the
      // checklist was shown. (showGettingStarted is only pinned explicitly
      // from Settings, and a completed core means there is nothing to show.)
      if done {
        model.dismissOnboarding()
      }
    }
  }

  private var onboardingFooter: String {
    if model.isOnboardingCoreComplete {
      return "Proxy Hub is optional — you can hide this list."
    }
    return "Save a login, then point an app at it. Proxy Hub is optional."
  }

  private func connectionSummary(_ app: DesignApp) -> String {
    if let name = app.routeOptions.first(where: { $0.id == app.selectedRouteID })?.name {
      return "Uses \(name)"
    }
    return "No login chosen"
  }

  private func transportLabel(_ app: DesignApp) -> String {
    transportDisplayName(for: app)
  }

  private var runningServiceNames: [String] {
    var names: [String] = []
    if model.proxyHubEnabled { names.append("Proxy Hub") }
    names.append(contentsOf: model.proxies.filter { $0.status == .running }.map(\.name))
    return names
  }

  private var servicesSummary: String {
    let names = runningServiceNames
    guard !names.isEmpty else { return "Services are stopped." }
    switch names.count {
    case 1: return names[0]
    case 2: return "\(names[0]) and \(names[1])"
    default:
      return names.dropLast().joined(separator: ", ") + ", and " + names[names.count - 1]
    }
  }

  private var serviceStatusText: String {
    let running = runningServiceNames.count
    let total = model.proxies.count + 1
    return "\(running) of \(total) running"
  }

  @ViewBuilder
  private func setupRow(
    done: Bool,
    title: String,
    detail: String,
    actionTitle: String,
    identifier: String,
    action: @escaping () -> Void
  ) -> some View {    LabeledContent {
      if done {
        Text("Done")
          .foregroundStyle(.secondary)
      } else {
        Button(actionTitle, action: action)
      }
    } label: {
      VStack(alignment: .leading, spacing: 2) {
        Text(title)
        Text(detail)
          .foregroundStyle(.secondary)
      }
    }
    .accessibilityIdentifier(identifier)
    .accessibilityValue(done ? "Done" : "Not done")
  }

  private func appIssue(_ app: DesignApp) -> String? {
    if !isAppRouteConfigured(app) {
      return "No login chosen. Open the app to choose one."
    }
    return app.issue
  }

  private var primaryAttention:
    (title: String, message: String, actionTitle: String, action: () -> Void)?
  {
    if let proxy = model.proxies.first(where: {
      $0.status == .attention || $0.status == .unavailable
    }) {
      return (
        "\(proxy.name) is unavailable",
        "Apps using \(proxy.name) cannot connect.",
        "Open Proxies",
        { model.destination = .proxies }
      )
    }
    if let app = model.apps.first(where: {
      $0.status == .attention || $0.status == .unavailable
    }) {
      return (
        "\(app.name) doesn’t have a working login",
        "Choose a login on Apps.",
        "Open Apps",
        { model.destination = .apps }
      )
    }
    if let choice = model.routingConflicts.first(where: { $0.kind == .sourceChoice }) {
      return (
        "Choose a \(choice.title) account",
        choice.detail,
        "Open Proxy Hub",
        { model.destination = .proxyHub }
      )
    }
    if let overlap = model.routingConflicts.first(where: { $0.kind == .modelOverlap }) {
      return (
        "Overlapping models",
        overlap.detail,
        "Open Proxy Hub",
        { model.destination = .proxyHub }
      )
    }
    return nil
  }
}
