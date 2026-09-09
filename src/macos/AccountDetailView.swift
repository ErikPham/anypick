import SwiftUI

struct AccountDetailView: View {
  @ObservedObject var model: MacOSDesignModel
  let accountID: String
  @State private var editing = false
  @State private var confirmingRemoval = false
  @State private var removalRequested = false
  @Environment(\.dismiss) private var dismiss

  private var account: DesignAccount? { model.accounts.first { $0.id == accountID } }
  private var usageWindows: [DesignUsageWindow] {
    guard let account else { return [] }
    return model.usageWindows(for: account)
  }

  var body: some View {
    DesignGroupedPane {
      if let account {
        Section {
          Label {
            VStack(alignment: .leading, spacing: 2) {
              Text(disambiguatedAccountName(for: account, in: model.accounts))
              let identity = accountIdentityLine(provider: account.provider, detail: account.detail)
              if !identity.isEmpty {
                Text(identity)
                  .foregroundStyle(.secondary)
              }
            }
          } icon: {
            DesignProviderIcon(provider: account.provider, fallbackSymbol: account.symbol)
          }
        }

        Section {
          LabeledContent("Provider", value: account.provider)
          LabeledContent("Type", value: account.kind == .gateway ? "API Gateway" : "Saved Login")
          LabeledContent("Status") {
            if model.isCurrentAccount(account) {
              Text("Current")
                .foregroundStyle(.secondary)
            } else {
              DesignStatusLabel(status: account.status)
            }
          }
        } footer: {
          if usageWindows.isEmpty {
            detailActions(for: account)
          }
        }

        if !usageWindows.isEmpty {
          Section {
            ForEach(usageWindows) { window in
              usageRow(window)
            }
          } header: {
            Text("Remaining Quotas")
          } footer: {
            VStack(alignment: .leading) {
              Text("Reported by \(account.provider) for the account currently active on this Mac.")
              detailActions(for: account)
            }
          }
        }
      }
    }
    .navigationTitle(account?.name ?? "Account")
    .sheet(isPresented: $editing) {
      EditAccountSheet(model: model, accountID: accountID)
    }
    .confirmationDialog(
      account.map { "Remove “\($0.name)” from AnyPick?" } ?? "Remove Account?",
      isPresented: $confirmingRemoval,
      titleVisibility: .visible
    ) {
      Button("Remove Account", role: .destructive) {
        removalRequested = true
        model.removeAccount(id: accountID)
      }
      Button("Cancel", role: .cancel) {}
    } message: {
      Text(removalMessage)
    }
    .onChange(of: model.accounts) { _, accounts in
      if removalRequested, !accounts.contains(where: { $0.id == accountID }) {
        dismiss()
      }
    }
    .onChange(of: model.isBusy) { wasBusy, busy in
      if removalRequested, wasBusy, !busy, account != nil {
        removalRequested = false
      }
    }
  }

  private func detailActions(for account: DesignAccount) -> some View {
    DesignPaneActions {
      DesignSheetButton(title: "Edit…") { editing = true }
      if account.kind == .saved && !model.isCurrentAccount(account) {
        DesignSheetButton(
          title: "Make Current",
          enabled: !model.isBusy,
          identifier: "accounts.inspector.make-current"
        ) { model.makeAccountCurrent(id: account.id) }
      }
      DesignSheetButton(
        title: account.kind == .gateway ? "Test Connection" : "Refresh Login",
        enabled: !model.isBusy,
        identifier: "accounts.inspector.test"
      ) {
        if account.kind == .gateway {
          model.testSavedGateway(id: account.id)
        } else {
          model.refreshAccount(id: account.id)
        }
      }
      DesignSheetButton(
        title: "Remove Account…",
        enabled: !model.isBusy,
        destructive: true,
        identifier: "accounts.inspector.remove"
      ) { confirmingRemoval = true }
    }
  }

  private func usageRow(_ window: DesignUsageWindow) -> some View {
    VStack(alignment: .leading, spacing: 6) {
      LabeledContent(window.label) {
        Text("\(window.remainingPercent)% remaining")
          .foregroundStyle(.secondary)
          .monospacedDigit()
      }
      ProgressView(value: Double(window.remainingPercent), total: 100)
      if let resetsAtMs = window.resetsAtMs {
        Text("Resets \(Date(timeIntervalSince1970: resetsAtMs / 1000), style: .relative)")
          .font(.caption)
          .foregroundStyle(.secondary)
      }
    }
    .accessibilityIdentifier("accounts.inspector.usage.\(window.id)")
  }

  private var removalMessage: String {
    guard let account else {
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
}
