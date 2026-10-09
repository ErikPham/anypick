import SwiftUI

struct FirstRunWelcomeSheet: View {
  @ObservedObject var model: MacOSDesignModel

  var body: some View {
    DesignSheetHost(
      title: "Welcome to AnyPick",
      width: 480,
      primaryTitle: "Add Login…",
      cancelTitle: "Not Now",
      onCancel: model.dismissFirstRunWelcome,
      onPrimary: model.addLoginFromWelcome
    ) {
      Form {
        Section {
          Text("Use the AI accounts you already have with the coding apps you use. Logins are stored on this Mac.")
            .font(.subheadline)
            .foregroundStyle(.secondary)
        }

        Section {
          welcomeRow(
            title: "Local by default",
            detail: "Credentials stay on this Mac",
            symbol: "lock.laptopcomputer"
          )
          welcomeRow(
            title: "Flexible connections",
            detail: "Provider accounts and API gateways",
            symbol: "arrow.triangle.branch"
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
          .font(.body.weight(.medium))
        Text(detail)
          .font(.subheadline)
          .foregroundStyle(.secondary)
      }
    } icon: {
      Image(systemName: symbol)
        .font(.title3)
        .foregroundStyle(Color.accentColor)
        .frame(width: 28, height: 28)
    }
    .padding(.vertical, 2)
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
                  .fontWeight(.medium)
                Text(condition.detail)
                  .font(.subheadline)
                  .foregroundStyle(.secondary)
              }
            } icon: {
              DesignWarningSymbol(scale: .medium)
            }
          }
        }
        .accessibilityIdentifier("overview.degraded-conditions")
      }

      if let attention = primaryAttention {
        Section {
          LabeledContent {
            Button(attention.actionTitle, action: attention.action)
              .buttonStyle(.bordered)
              .controlSize(.small)
          } label: {
            Label {
              VStack(alignment: .leading, spacing: 2) {
                Text(attention.title)
                  .fontWeight(.medium)
                Text(attention.message)
                  .font(.subheadline)
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
            title: "Add a Login",
            detail: model.hasSavedLogin
              ? "A login is saved on this Mac."
              : "Use a login already on this Mac.",
            actionTitle: "Add Login…",
            identifier: "overview.onboarding.save-login"
          ) {
            model.requestAddLogin()
          }
          setupRow(
            done: model.hasInstalledApp,
            title: "Use an App",
            detail: model.hasInstalledApp
              ? "Claude Code, Codex, or another supported app is available."
              : "Install Claude Code or Codex, then refresh.",
            actionTitle: "Refresh",
            identifier: "overview.onboarding.use-app",
            // Single-flight, like every other command in the app: a second
            // refresh while one is in flight lands on the same pipe.
            isEnabled: !model.isBusy
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
          Button("Hide Getting Started") { model.dismissOnboarding() }
            .font(.subheadline)
            .foregroundStyle(.secondary)
            .accessibilityIdentifier("overview.hide-onboarding")
        } header: {
          // Identify the header only: an identifier on the Section would
          // overwrite every row's own overview.onboarding.* identifier.
          Text("Get Started")
            .accessibilityIdentifier("overview.get-started")
        } footer: {
          Text(onboardingFooter)
        }
      }

      // Avoid an empty section above the checklist on first run.
      if !overviewApps.isEmpty {
        Section {
          ForEach(overviewApps) { app in
            NavigationLink(value: DesignRouteID.app(app.id)) {
              DesignIdentityRow(
                title: app.name,
                detail: connectionSummary(app),
                issue: appIssue(app)
              ) {
                DesignProviderIcon(provider: app.id, fallbackSymbol: app.symbol)
              } trailing: {
                HStack(spacing: 6) {
                  if !isAppRouteConfigured(app) {
                    Image(systemName: "circle.fill")
                      .font(.system(size: 7))
                      .foregroundStyle(.orange)
                  }
                  Text(transportLabel(app))
                    .font(.subheadline)
                    .foregroundStyle(isAppRouteConfigured(app) ? Color.secondary : Color.orange)
                }
              }
            }
            .accessibilityIdentifier("overview.app.\(app.id)")
            .accessibilityLabel("\(app.name), \(connectionSummary(app)), \(transportLabel(app))")
          }

          if model.apps.count > overviewApps.count {
            Button {
              model.destination = .apps
            } label: {
              HStack {
                Text("All Apps (\(model.apps.count))…")
                  .foregroundStyle(Color.accentColor)
                Spacer()
                Image(systemName: "chevron.right")
                  .font(.caption.weight(.semibold))
                  .foregroundStyle(.tertiary)
              }
              .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("overview.apps.see-all")
          }
        } header: {
          Text(hasActiveOverviewApps ? "Active Apps" : "Apps")
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
            .font(.title3)
            .foregroundStyle(runningServiceNames.isEmpty ? Color.secondary : Color.accentColor)
        } trailing: {
          HStack(spacing: 6) {
            if !runningServiceNames.isEmpty {
              Image(systemName: "circle.fill")
                .font(.system(size: 7))
                .foregroundStyle(.green)
            }
            Text(serviceStatusText)
              .font(.subheadline)
              .foregroundStyle(.secondary)
              .monospacedDigit()
          }
        }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("overview.services-summary")
      } header: {
        Text("Services")
      }

      // While the checklist is visible, defer an empty activity section until
      // onboarding is complete.
      if !model.activity.isEmpty || !model.shouldShowOnboarding {
        Section {
          if model.activity.isEmpty {
            DesignEmptyRow(text: "Recent events will appear here.")
          } else {
            ForEach(model.activity.prefix(3)) { item in
              HStack(alignment: .center, spacing: 12) {
                if let provider = item.provider {
                  DesignProviderIcon(provider: provider, fallbackSymbol: item.symbol, size: 18)
                } else {
                  Image(
                    systemName: item.status == .ready || item.status == .running
                      ? item.symbol : item.status.symbol
                  )
                  .font(.system(size: 14))
                  .foregroundStyle(item.status.color)
                  .frame(width: 18)
                }
                VStack(alignment: .leading, spacing: 2) {
                  Text(item.title)
                    .font(.body.weight(.medium))
                    .lineLimit(1)
                  if !item.detail.isEmpty {
                    Text(item.detail)
                      .font(.caption)
                      .foregroundStyle(.secondary)
                      .lineLimit(1)
                  }
                }
                Spacer(minLength: 8)
                Text(item.time)
                  .font(.subheadline)
                  .foregroundStyle(.secondary)
                  .monospacedDigit()
              }
              .padding(.vertical, 2)
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
    overviewConnectionSummary(app)
  }

  private func transportLabel(_ app: DesignApp) -> String {
    transportDisplayName(for: app)
  }

  private var hasActiveOverviewApps: Bool {
    model.apps.contains { app in
      isAppManagedByAnyPick(app) || app.status == .attention || app.status == .unavailable
    }
  }

  private var overviewApps: [DesignApp] {
    let active = model.apps.filter { app in
      isAppManagedByAnyPick(app) || app.status == .attention || app.status == .unavailable
    }
    if !active.isEmpty {
      return Array(active.prefix(3))
    }
    return Array(model.apps.prefix(3))
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
    let total = overviewServiceTotal(
      proxyCount: model.proxies.count,
      hubSourcesEmpty: model.hubSources.isEmpty,
      hubEnabled: model.proxyHubEnabled
    )
    return "\(running) of \(total) running"
  }

  @ViewBuilder
  private func setupRow(
    done: Bool,
    title: String,
    detail: String,
    actionTitle: String,
    identifier: String,
    isEnabled: Bool = true,
    action: @escaping () -> Void
  ) -> some View {
    LabeledContent {
      if done {
        Label("Done", systemImage: "checkmark")
          .labelStyle(.titleAndIcon)
          .font(.subheadline)
          .foregroundStyle(.secondary)
      } else {
        Button(actionTitle, action: action)
          .buttonStyle(.bordered)
          .controlSize(.small)
          .disabled(!isEnabled)
          .help(actionTitle)
      }
    } label: {
      Label {
        VStack(alignment: .leading, spacing: 2) {
          Text(title)
            .font(.body.weight(.medium))
          Text(detail)
            .font(.subheadline)
            .foregroundStyle(.secondary)
        }
      } icon: {
        Image(systemName: done ? "checkmark.circle.fill" : "circle")
          .font(.system(size: 14))
          .foregroundStyle(done ? Color.green : Color.secondary)
      }
    }
    .padding(.vertical, 1)
    .accessibilityIdentifier(identifier)
    .accessibilityValue(done ? "Done" : "Not done")
  }

  private func appIssue(_ app: DesignApp) -> String? {
    if !isAppRouteConfigured(app) {
      return "No login chosen. Open the app to choose one."
    }
    return app.issue
  }

  private var actionableRoutingConflicts: [DesignRoutingConflict] {
    model.routingConflicts.filter { conflict in
      let providers = Set(conflict.candidates.map { hubProviderDisplayName($0.provider) }.filter { !$0.isEmpty })
      return providers.count > 1
    }
  }

  private var primaryAttention:
    (title: String, message: String, actionTitle: String, action: () -> Void)?
  {
    // Keep priority in the shared helper so fixture and production render the
    // same recovery destination.
    guard
      let target = overviewAttentionTarget(
        proxies: model.proxies,
        apps: model.apps,
        accounts: model.accounts,
        conflicts: actionableRoutingConflicts
      )
    else { return nil }
    switch target {
    case .proxy(let id):
      guard let proxy = model.proxies.first(where: { $0.id == id }) else { return nil }
      return (
        "\(proxy.name) is unavailable",
        "Apps using \(proxy.name) cannot connect.",
        "Open Proxies",
        { model.destination = .proxies }
      )
    case .app(let id):
      guard let app = model.apps.first(where: { $0.id == id }) else { return nil }
      return (
        "\(app.name) doesn’t have a working login",
        "Choose a login on Apps.",
        "Open Apps",
        { model.destination = .apps }
      )
    case .account(let id):
      guard let account = model.accounts.first(where: { $0.id == id }) else { return nil }
      return (
        "\(account.name) isn’t working",
        "Check the login on Accounts & APIs.",
        "Open Accounts",
        { model.destination = .accounts }
      )
    case .conflict(let kind):
      guard let conflict = actionableRoutingConflicts.first(where: { $0.kind == kind }) ?? actionableRoutingConflicts.first else {
        return nil
      }
      let (infoTitle, infoSubtitle) = hubConflictDisplayInfo(conflict)
      return (
        "Model conflict: \(infoTitle)",
        infoSubtitle,
        "Open Proxy Hub",
        { model.destination = .proxyHub }
      )
    }
  }
}
