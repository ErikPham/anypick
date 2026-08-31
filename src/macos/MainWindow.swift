import AppKit
import SwiftUI

/// System Settings shell: sidebar search + List, detail is a NavigationStack.
struct MainWindow: View {
  @ObservedObject var model: MacOSDesignModel
  @Environment(\.openWindow) private var openWindow
  @State private var sidebarSearch = ""
  @State private var path = NavigationPath()

  private var filteredDestinations: [DesignDestination] {
    guard !sidebarSearch.isEmpty else { return DesignDestination.allCases }
    return DesignDestination.allCases.filter {
      $0.title.localizedCaseInsensitiveContains(sidebarSearch)
    }
  }

  var body: some View {
    NavigationSplitView {
      sidebarColumn
        .navigationSplitViewColumnWidth(min: 200, ideal: 216, max: 260)
        .toolbar(removing: .sidebarToggle)
        .safeAreaInset(edge: .bottom, spacing: 0) { sidebarServiceStatus }
    } detail: {
      detailColumn
        .frame(minWidth: 560, maxWidth: .infinity, maxHeight: .infinity)
    }
    .searchable(text: $sidebarSearch, placement: .sidebar, prompt: "Search")
    .alert(
      "Unable to Complete",
      isPresented: Binding(
        get: { model.feedback?.kind == .error },
        set: { if !$0 { model.dismissFeedback() } }
      )
    ) {
      Button("OK", role: .cancel, action: model.dismissFeedback)
    } message: {
      Text(model.feedback?.message ?? "")
    }
    .followingDesignAppearance(model.appearance)
    .frame(minWidth: 820, minHeight: 560)
    .sheet(
      isPresented: Binding(
        get: { model.showFirstRunWelcome },
        set: { if !$0 { model.dismissFirstRunWelcome() } }
      )
    ) {
      FirstRunWelcomeSheet(model: model)
    }
    .onAppear {
      presentStartupRoute()
      if !model.isProduction {
        openWindow(id: "tray")
      }
    }
    .onChange(of: model.destination) { path = NavigationPath() }
    .onChange(of: model.announcementRequested) {
      NSAccessibility.post(
        element: NSApplication.shared,
        notification: .announcementRequested
      )
    }
  }

  @ViewBuilder
  private var sidebarColumn: some View {
    List(selection: $model.destination) {
      if filteredDestinations.isEmpty {
        ContentUnavailableView.search(text: sidebarSearch)
      } else {
        ForEach(filteredDestinations) { destination in
          sidebarRow(destination)
        }
      }
    }
    .listStyle(.sidebar)
    .accessibilityIdentifier("sidebar.navigation")
  }

  private func sidebarRow(_ destination: DesignDestination) -> some View {
    Label {
      Text(destination.title)
    } icon: {
      Image(systemName: destination.symbol)
        .symbolRenderingMode(.hierarchical)
    }
    .tag(destination)
    .accessibilityIdentifier("sidebar.\(destination.rawValue)")
  }

  private func presentStartupRoute() {
    guard model.pendingStartupFlow == "published-models" else { return }
    model.pendingStartupFlow = nil
    path.append(DesignRouteID.publishedModels)
  }

  /// Sidebar footer service status — "2 proxies running".
  private var serviceStatusSummary: String {
    let running = model.proxies.filter(\.enabled).count + (model.proxyHubEnabled ? 1 : 0)
    if running == 0 { return "No proxies running" }
    return running == 1 ? "1 proxy running" : "\(running) proxies running"
  }

  @ViewBuilder
  private var sidebarServiceStatus: some View {
    if model.phase == .ready || model.phase == .empty {
      VStack(spacing: 0) {
        Divider()
        Label {
          Text(serviceStatusSummary)
        } icon: {
          Image(systemName: DesignDestination.proxies.symbol)
        }
        .font(.footnote)
        .foregroundStyle(.secondary)
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 16)
        .padding(.vertical, 8)
      }
      .accessibilityElement(children: .combine)
      .accessibilityIdentifier("sidebar.service-status")
    }
  }

  // kickSplitViewLayout() used to live here. It nudged the window frame by +1px
  // and back to force NSHostingView to lay the sidebar out. `.onAppear` re-runs
  // whenever NavigationStack rebuilds on a `model.destination` change, so every
  // sidebar click bumped the window by one pixel — the "layout jump" the sidebar
  // had. The empty-sidebar symptom it worked around does not reproduce on the
  // macOS releases we ship; if it ever comes back, reintroduce it behind a
  // one-shot guard (a `@State` flag) so it can only ever fire once.

  @ViewBuilder
  private var detailColumn: some View {
    switch model.phase {
    case .loading:
      DesignLoadingState()
        .navigationTitle("AnyPick")
    case .error:
      DesignEmptyState(
        title: "AnyPick could not load",
        message: model.isProduction
          ? "The bundled AnyPick service did not provide a valid snapshot. Try again or reinstall AnyPick."
          : "The design fixture failed to prepare its in-memory data.",
        symbol: "exclamationmark.triangle",
        actionTitle: "Try Again",
        action: model.refresh
      )
      .navigationTitle("AnyPick")
      .accessibilityIdentifier("state.error")
    case .ready, .empty:
      NavigationStack(path: $path) {
        rootPane
          .navigationTitle(model.destination.title)
          .navigationDestination(for: DesignRouteID.self) { route in
            switch route {
            case .app(let id):
              AppDetailView(model: model, appID: id)
            case .account(let id):
              if let account = model.accounts.first(where: { $0.id == id }) {
                AccountDetailView(model: model, accountID: account.id)
              }
            case .proxy(let id):
              ProxyDetailView(model: model, proxyID: id)
            case .proxyHub:
              ProxyHubView(model: model)
            case .publishedModels:
              PublishedModelsView(model: model)
            }
          }
      }
    }
  }

  @ViewBuilder
  private var rootPane: some View {
    switch model.destination {
    case .overview: OverviewView(model: model)
    case .apps: AppsView(model: model)
    case .accounts: AccountsView(model: model)
    case .proxyHub: ProxyHubView(model: model)
    case .proxies: ProxiesView(model: model)
    case .activity: ActivityView(model: model)
    case .backups: BackupsView(model: model)
    }
  }
}

enum DesignRouteID: Hashable {
  case app(String)
  case account(String)
  case proxy(String)
  case proxyHub
  case publishedModels
}
