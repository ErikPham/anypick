import AppKit
import SwiftUI

private enum AccountProviderFilter: Hashable {
  case all
  case provider(String)
}

struct AccountsView: View {
  @ObservedObject var model: MacOSDesignModel
  @State private var activeSheet: AccountsSheet?
  @State private var filter: AccountProviderFilter = .all
  @State private var searchQuery = ""
  @State private var editingAccount: DesignAccount?
  @State private var connectingGateway: DesignAccount?
  @State private var pendingRemoval: DesignAccount?
  @State private var hoveredAccountID: String?

  init(model: MacOSDesignModel) {
    self.model = model
    let startupSheet: AccountsSheet? =
      switch model.pendingStartupFlow {
      case "add-account": .addAccount
      case "add-gateway": .addGateway
      default: nil
      }
    _activeSheet = State(initialValue: startupSheet)
    _pendingRemoval = State(
      initialValue: model.pendingStartupFlow == "remove-account"
        ? model.accounts.first(where: { $0.kind == .saved })
        : nil
    )
  }

  private var providers: [String] {
    let names = Set(model.accounts.map(\.provider))
    return names.sorted { lhs, rhs in
      let lhsPriority = nativeProviderPriority(lhs)
      let rhsPriority = nativeProviderPriority(rhs)
      if lhsPriority != rhsPriority { return lhsPriority < rhsPriority }
      return lhs.localizedStandardCompare(rhs) == .orderedAscending
    }
  }

  private var savedAccounts: [DesignAccount] {
    let filtered =
      switch filter {
      case .all:
        model.accounts.filter { $0.kind == .saved }
      case .provider(let provider):
        model.accounts.filter { $0.kind == .saved && $0.provider == provider }
      }
    return searched(filtered).sorted(by: nativeAccountOrder)
  }

  private var gateways: [DesignAccount] {
    searched(model.accounts.filter { $0.kind == .gateway }).sorted(by: nativeAccountOrder)
  }

  var body: some View {
    Group {
      if model.accounts.isEmpty {
        DesignEmptyState(
          title: "No Accounts",
          message: "Add the login already on this Mac, sign in, or add an API gateway.",
          symbol: "person.badge.plus",
          actionTitle: "Add Account…",
          action: { activeSheet = .addAccount }
        )
      } else if savedAccounts.isEmpty && gateways.isEmpty {
        DesignEmptyState(
          title: "No Results",
          message: emptyFilterMessage,
          symbol: "magnifyingglass",
          actionTitle: hasActiveFilter ? "Clear Filters" : nil,
          action: hasActiveFilter ? clearFilters : nil
        )
      } else {
        inventory
      }
    }
    .toolbar {
      ToolbarItem(placement: .primaryAction) {
        DesignToolbarSearch(text: $searchQuery, prompt: "Search Accounts")
          .frame(minWidth: 180, idealWidth: 220, maxWidth: 280)
      }
    }
    .accessibilityIdentifier("accounts.list")
    .sheet(item: $activeSheet) { sheet in
      switch sheet {
      case .addAccount: AddSavedAccountSheet(model: model)
      case .addGateway: AddGatewaySheet(model: model)
      }
    }
    .sheet(item: $editingAccount) { account in
      EditAccountSheet(model: model, accountID: account.id)
    }
    .sheet(item: $connectingGateway) { gateway in
      ConnectGatewaySheet(model: model, gatewayID: gateway.id)
    }
    .onChange(of: model.pendingGatewayConnectID) { _, id in
      guard let id, let gateway = model.accounts.first(where: { $0.id == id }) else { return }
      model.pendingGatewayConnectID = nil
      connectingGateway = gateway
    }
    .confirmationDialog(
      pendingRemoval.map { "Remove “\($0.name)” from AnyPick?" } ?? "Remove Account?",
      isPresented: Binding(
        get: { pendingRemoval != nil },
        set: { if !$0 { pendingRemoval = nil } }
      ),
      titleVisibility: .visible
    ) {
      Button("Remove Account", role: .destructive) {
        guard let account = pendingRemoval else { return }
        model.removeAccount(id: account.id)
        pendingRemoval = nil
      }
      Button("Cancel", role: .cancel) {}
    } message: {
      Text(removalMessage)
    }
    // Consume the entry flow once (B1-family): without this, every return to
    // the pane re-opens the entry sheet from a stale flag.
    .onAppear { model.pendingStartupFlow = nil }
  }

  private var inventory: some View {
    DesignGroupedPane {
      Section {
        if savedAccounts.isEmpty {
          ContentUnavailableView {
            Label("No Accounts", systemImage: "person.2")
          } description: {
            Text(emptyFilterMessage)
          }
        } else {
          ForEach(savedAccounts) { account in
            accountRow(account)
          }
        }
      } header: {
        HStack {
          Text("Accounts")
          Spacer()
          providerPicker
        }
      } footer: {
        if gateways.isEmpty {
          footerActions
        }
      }

      if !gateways.isEmpty {
        Section {
          ForEach(gateways) { gateway in
            accountRow(gateway)
          }
        } header: {
          Text("API Gateways")
        } footer: {
          footerActions
        }
      }
    }
    .onChange(of: providers) { _, names in
      if case .provider(let provider) = filter, !names.contains(provider) {
        filter = .all
      }
    }
  }

  private var footerActions: some View {
    DesignPaneActions {
      DesignSheetButton(
        title: "Add Account…",
        identifier: "accounts.save-login"
      ) { activeSheet = .addAccount }
      DesignSheetButton(
        title: "Add API Gateway…",
        enabled: canAddGateway(
          isBusy: model.isBusy,
          gatewayProviderCount: model.gatewayProviderOptions.count
        ),
        identifier: "accounts.add-gateway"
      ) { activeSheet = .addGateway }
    }
  }

  private func accountRow(_ account: DesignAccount) -> some View {
    DesignIdentityRow(
      title: disambiguatedAccountName(for: account, in: model.accounts),
      detail: accountIdentityLine(provider: account.provider, detail: account.detail),
      issue: accountIssue(account)
    ) {
      DesignProviderIcon(provider: account.provider, fallbackSymbol: account.symbol)
    } trailing: {
      rowTrailing(account)
    }
    .accessibilityIdentifier("accounts.row.\(account.id)")
    .accessibilityLabel(accountAccessibilityLabel(for: account, in: model.accounts))
    .onHover { hovering in
      hoveredAccountID = hovering ? account.id : (hoveredAccountID == account.id ? nil : hoveredAccountID)
    }
    .contextMenu {
      Button("Edit") { editingAccount = account }
      if account.kind == .saved && !model.isCurrentAccount(account) {
        Button("Make Current") { model.makeAccountCurrent(id: account.id) }
          .disabled(model.isBusy)
      }
      if account.kind == .gateway {
        Button("Connect") { connectingGateway = account }
          .disabled(model.isBusy || account.status == .unavailable)
      }
      Divider()
      Button("Remove", role: .destructive) { pendingRemoval = account }
    }
    .modifier(
      AccountRowAccessibilityActions(
        showsSwitch: account.kind == .saved && !model.isCurrentAccount(account),
        showsConnect: account.kind == .gateway,
        onSwitch: { model.makeAccountCurrent(id: account.id) },
        onConnect: { connectingGateway = account }
      )
    )
  }

  // Design U2MnSV/sBdJe “Aligned Action Grid”: fixed columns — Switch slot,
  // status slot, then the NavigationLink chevron. Hover reveals
  // Switch/Connect; opacity (not removal) keeps the columns aligned.
  // No "..." menu: it was deleted deliberately (it leaked into the toolbar).
  @ViewBuilder
  private func rowTrailing(_ account: DesignAccount) -> some View {
    let switching = model.switchingAccountID == account.id
    let showsSwitch = account.kind == .saved && !model.isCurrentAccount(account)
    let showsConnect = account.kind == .gateway
    let visible = hoveredAccountID == account.id || switching

    HStack(spacing: 9) {
      ZStack {
        if showsSwitch {
          Button {
            model.makeAccountCurrent(id: account.id)
          } label: {
            if switching { ProgressView().controlSize(.small) } else { Text("Switch") }
          }
          .disabled(model.isBusy)
          .accessibilityIdentifier("accounts.switch.\(account.id)")
        } else if showsConnect {
          Button {
            connectingGateway = account
          } label: {
            if switching { ProgressView().controlSize(.small) } else { Text("Connect") }
          }
          .disabled(model.isBusy || account.status == .unavailable)
          .accessibilityIdentifier("accounts.connect.\(account.id)")
        }
      }
      .buttonStyle(.bordered)
      .controlSize(.small)
      .frame(minWidth: 64, minHeight: 26)
      .opacity(visible ? 1 : 0)
      .allowsHitTesting(visible)

      ZStack {
        Color.clear
        if model.isCurrentAccount(account) {
          Image(systemName: "checkmark.circle.fill")
            .foregroundStyle(.green)
            .accessibilityLabel("Current")
        }
      }
      .frame(width: 18, height: 18)

      EllipsisCircleMenu(
        accessibilityName: "More actions for \(accountAccessibilityLabel(for: account, in: model.accounts))",
        identifier: "accounts.more.\(account.id)",
        onDetail: { model.pendingAccountDetailID = account.id },
        onEdit: { editingAccount = account },
        onRemove: { pendingRemoval = account }
      )
      .frame(width: 19, height: 19)
    }
    .fixedSize()
  }

  private func accountIssue(_ account: DesignAccount) -> String? {
    switch account.status {
    case .attention, .unavailable:
      return account.status.rawValue
    case .ready, .running, .stopped:
      return nil
    }
  }

  private var providerPicker: some View {
    Picker("Provider", selection: $filter) {
      Text("All Providers").tag(AccountProviderFilter.all)
      ForEach(providers, id: \.self) { provider in
        Text(provider).tag(AccountProviderFilter.provider(provider))
      }
    }
    .pickerStyle(.menu)
    .labelsHidden()
    .fixedSize()
    .accessibilityIdentifier("accounts.filter")
  }

  private var removalMessage: String {
    guard let account = pendingRemoval else {
      return "This account will no longer be available in AnyPick."
    }
    if account.kind == .gateway {
      return "Only “\(account.name)” is removed from AnyPick. The remote API key is not revoked."
    }
    let app = anypickDisplayName(account.provider)
    if model.isCurrentAccount(account) {
      return "“\(account.name)” is removed from AnyPick. \(app) stays signed in on this Mac."
    }
    return "“\(account.name)” is removed from AnyPick. The \(app) login on this Mac does not change."
  }

  private var hasActiveFilter: Bool {
    !searchQuery.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
      || filter != .all
  }

  private func clearFilters() {
    searchQuery = ""
    filter = .all
  }

  private var emptyFilterMessage: String {
    if !searchQuery.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
      return "No accounts match \"\(searchQuery)\"."
    }
    if case .provider(let provider) = filter {
      return "No \(provider) accounts."
    }
    return "No accounts match this filter."
  }

  private func searched(_ accounts: [DesignAccount]) -> [DesignAccount] {
    let query = searchQuery.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !query.isEmpty else { return accounts }
    return accounts.filter {
      [$0.name, $0.detail, $0.provider, $0.kind.rawValue]
        .contains { $0.localizedCaseInsensitiveContains(query) }
    }
  }

  private func nativeAccountOrder(_ lhs: DesignAccount, _ rhs: DesignAccount) -> Bool {
    if lhs.kind != rhs.kind { return lhs.kind == .saved }
    let lhsIndex = nativeProviderPriority(lhs.provider)
    let rhsIndex = nativeProviderPriority(rhs.provider)
    if lhsIndex != rhsIndex { return lhsIndex < rhsIndex }
    if model.isCurrentAccount(lhs) != model.isCurrentAccount(rhs) {
      return model.isCurrentAccount(lhs)
    }
    return lhs.name.localizedStandardCompare(rhs.name) == .orderedAscending
  }
}

/// "..." menu trigger. NSButton, not a SwiftUI Button/Menu: on macOS 26 a
/// SwiftUI button under a toolbar search is copied into the titlebar.
/// Menu items: Detail, Edit, Remove.
private struct EllipsisCircleMenu: NSViewRepresentable {
  var accessibilityName: String
  var identifier: String
  var onDetail: () -> Void
  var onEdit: () -> Void
  var onRemove: () -> Void

  func makeCoordinator() -> Coordinator {
    Coordinator(onDetail: onDetail, onEdit: onEdit, onRemove: onRemove)
  }

  func makeNSView(context: Context) -> NSButton {
    let button = NSButton()
    button.isBordered = false
    button.bezelStyle = .inline
    button.setButtonType(.momentaryChange)
    button.imagePosition = .imageOnly
    button.imageScaling = .scaleProportionallyDown
    button.image = NSImage(
      systemSymbolName: "ellipsis.circle",
      accessibilityDescription: accessibilityName
    )
    button.contentTintColor = .secondaryLabelColor
    button.target = context.coordinator
    button.action = #selector(Coordinator.press(_:))
    button.setAccessibilityLabel(accessibilityName)
    button.setAccessibilityIdentifier(identifier)
    return button
  }

  func updateNSView(_ button: NSButton, context: Context) {
    context.coordinator.onDetail = onDetail
    context.coordinator.onEdit = onEdit
    context.coordinator.onRemove = onRemove
    button.setAccessibilityLabel(accessibilityName)
    button.setAccessibilityIdentifier(identifier)
  }

  final class Coordinator: NSObject {
    var onDetail: () -> Void
    var onEdit: () -> Void
    var onRemove: () -> Void

    init(
      onDetail: @escaping () -> Void, onEdit: @escaping () -> Void, onRemove: @escaping () -> Void
    ) {
      self.onDetail = onDetail
      self.onEdit = onEdit
      self.onRemove = onRemove
    }

    @objc func press(_ sender: NSButton) {
      let menu = NSMenu()
      menu.autoenablesItems = false
      let detail = menu.addItem(withTitle: "Detail", action: #selector(detail), keyEquivalent: "")
      detail.target = self
      let edit = menu.addItem(withTitle: "Edit", action: #selector(edit), keyEquivalent: "")
      edit.target = self
      menu.addItem(.separator())
      let remove = menu.addItem(withTitle: "Remove", action: #selector(remove), keyEquivalent: "")
      remove.target = self
      remove.identifier = NSUserInterfaceItemIdentifier("accounts.more.remove")
      menu.popUp(positioning: nil, at: NSPoint(x: 0, y: sender.bounds.height), in: sender)
    }

    @objc func detail() { onDetail() }
    @objc func edit() { onEdit() }
    @objc func remove() { onRemove() }
  }
}

private struct AccountRowAccessibilityActions: ViewModifier {
  let showsSwitch: Bool
  let showsConnect: Bool
  let onSwitch: () -> Void
  let onConnect: () -> Void

  func body(content: Content) -> some View {
    if showsSwitch {
      content.accessibilityAction(named: "Switch", onSwitch)
    } else if showsConnect {
      content.accessibilityAction(named: "Connect", onConnect)
    } else {
      content
    }
  }
}
