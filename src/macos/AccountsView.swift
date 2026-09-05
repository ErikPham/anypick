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
  @State private var inspectingAccount: DesignAccount?
  @State private var connectingGateway: DesignAccount?
  @State private var pendingRemoval: DesignAccount?

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
    .searchable(text: $searchQuery, placement: .toolbar, prompt: "Search Accounts")
    .accessibilityIdentifier("accounts.list")
    .toolbar {
      ToolbarItem(placement: .primaryAction) {
        Button("Add Account…") { activeSheet = .addAccount }
          .accessibilityIdentifier("accounts.toolbar.save-login")
      }
      ToolbarItem {
        Button("Add API Gateway…") { activeSheet = .addGateway }
          .accessibilityIdentifier("accounts.toolbar.add-gateway")
      }
    }
    .sheet(item: $activeSheet) { sheet in
      switch sheet {
      case .addAccount: AddSavedAccountSheet(model: model)
      case .addGateway: AddGatewaySheet(model: model)
      }
    }
    .sheet(item: $editingAccount) { account in
      EditAccountSheet(model: model, accountID: account.id)
    }
    .sheet(item: $inspectingAccount) { account in
      AccountInspectSheet(model: model, accountID: account.id)
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
  }

  private var inventory: some View {
    DesignGroupedPane {
      Section {
        if savedAccounts.isEmpty {
          Text(emptyFilterMessage)
            .foregroundStyle(.secondary)
        } else {
          ForEach(savedAccounts) { account in
            AccountInventoryRow(
              model: model,
              account: account,
              onInspect: { inspectingAccount = account },
              onEdit: { editingAccount = account },
              onRemove: { pendingRemoval = account }
            )
          }
        }
      } header: {
        HStack {
          Text("Accounts")
          Spacer()
          providerPicker
        }
      }

      if !gateways.isEmpty {
        Section("API Gateways") {
          ForEach(gateways) { gateway in
            AccountInventoryRow(
              model: model,
              account: gateway,
              onInspect: { inspectingAccount = gateway },
              onEdit: { editingAccount = gateway },
              onConnect: { connectingGateway = gateway },
              onRemove: { pendingRemoval = gateway }
            )
          }
        }
      }
    } actions: {
      Button("Add Account…") { activeSheet = .addAccount }
        .accessibilityIdentifier("accounts.save-login")
      Button("Add API Gateway…") { activeSheet = .addGateway }
        .accessibilityIdentifier("accounts.add-gateway")
    }
    .onChange(of: providers) { _, names in
      if case .provider(let provider) = filter, !names.contains(provider) {
        filter = .all
      }
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

private struct AccountInventoryRow: View {
  @ObservedObject var model: MacOSDesignModel
  let account: DesignAccount
  let onInspect: () -> Void
  var onEdit: () -> Void
  var onConnect: (() -> Void)? = nil
  var onRemove: () -> Void
  /// Trailing Switch/Connect stay hidden until hover (native list behavior).
  @State private var isHovered = false

  private var showsSwitch: Bool {
    account.kind == .saved && !model.isCurrentAccount(account)
  }

  private var showsConnect: Bool {
    account.kind == .gateway && onConnect != nil
  }

  /// Quota of the provider-reported windows for this account's login. Only the
  /// current account reports usage; rows without data stay clean.
  private var quotaLine: String? {
    guard let window = model.usageWindows(for: account).first else { return nil }
    var line = "\(window.remainingPercent)% left"
    if let resetsAtMs = window.resetsAtMs {
      let reset = Date(timeIntervalSince1970: resetsAtMs / 1000)
      let formatted = reset.formatted(
        date: Calendar.current.isDateInToday(reset) ? .omitted : .abbreviated,
        time: .shortened
      )
      line += " · resets \(formatted)"
    }
    return line
  }

  var body: some View {
    HStack(spacing: 10) {
      DesignProviderIcon(provider: account.provider, fallbackSymbol: account.symbol)
        .frame(width: 20, height: 20)
        .accessibilityHidden(true)
      Button(action: onInspect) {
        VStack(alignment: .leading, spacing: 2) {
          Text(disambiguatedAccountName(for: account, in: model.accounts))
          Text(accountIdentityLine(provider: account.provider, detail: account.detail))
            .foregroundStyle(.secondary)
            .lineLimit(1)
          if let quotaLine {
            Text(quotaLine)
              .font(.caption)
              .foregroundStyle(.secondary)
              .monospacedDigit()
              .lineLimit(1)
          }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
      }
      .buttonStyle(.plain)
      .accessibilityLabel(accountAccessibilityLabel(for: account, in: model.accounts))
      HStack(spacing: 9) {
        actionSlot
        statusSlot
        EllipsisCircleMenu(
          accessibilityName: "More actions for \(accountAccessibilityLabel(for: account, in: model.accounts))",
          identifier: "accounts.more.\(account.id)",
          onDetail: onInspect,
          onEdit: onEdit,
          onRemove: onRemove
        )
        .frame(width: 19, height: 19)
      }
      .fixedSize()
    }
    .contentShape(Rectangle())
    .onHover { hovering in isHovered = hovering }
    .contextMenu { rowMenu }
    .accessibilityElement(children: .combine)
    .accessibilityLabel(accountAccessibilityLabel(for: account, in: model.accounts))
    .accessibilityIdentifier("accounts.row.\(account.id)")
    .modifier(
      AccountRowAccessibilityActions(
        showsSwitch: showsSwitch,
        showsConnect: showsConnect,
        onSwitch: { model.makeAccountCurrent(id: account.id) },
        onConnect: { onConnect?() }
      )
    )
  }

  @ViewBuilder
  private var actionSlot: some View {
    // Switch/Connect reveal on hover only; the switching spinner always
    // shows. Opacity (not removal) keeps the row geometry stable.
    // HIG: while busy the button stays and spins inside it — it is never
    // swapped out for a bare spinner.
    let switching = model.switchingAccountID == account.id
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
          onConnect?()
        } label: {
          if switching { ProgressView().controlSize(.small) } else { Text("Connect") }
        }
        .disabled(model.isBusy || account.status == .unavailable)
        .accessibilityIdentifier("accounts.connect.\(account.id)")
      }
    }
    .buttonStyle(.bordered)
    .controlSize(.small)
    .opacity(switching || isHovered ? 1 : 0)
    .allowsHitTesting(switching || isHovered)
    .fixedSize()
  }

  private var statusSlot: some View {
    ZStack {
      Color.clear
      if model.isCurrentAccount(account)
        || (account.kind == .gateway && (account.status == .ready || account.status == .running))
      {
        Image(systemName: "checkmark.circle.fill")
          .foregroundStyle(.green)
          .accessibilityLabel(model.isCurrentAccount(account) ? "Current" : "Ready")
      } else if account.status == .attention {
        Image(systemName: DesignStatus.attention.symbol)
          .foregroundStyle(DesignStatus.attention.color)
          .accessibilityLabel(account.status.rawValue)
      } else if account.status == .unavailable {
        Image(systemName: DesignStatus.unavailable.symbol)
          .foregroundStyle(DesignStatus.unavailable.color)
          .accessibilityLabel(account.status.rawValue)
      }
    }
    .frame(width: 18, height: 18)
  }

  @ViewBuilder
  private var rowMenu: some View {
    Button("Detail") { onInspect() }
    Button("Edit") { onEdit() }
    Divider()
    Button("Remove", role: .destructive) { onRemove() }
  }
}

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
