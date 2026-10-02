import SwiftUI

/// How many accounts one backup selection may name. This is
/// transfer.MaxBackupAccounts: the sheet and the desktop protocol accept the
/// same set the backup file can hold.
enum BackupLimits {
  static let maxAccounts = 256
}

struct CreateBackupSheet: View {
  @ObservedObject var model: MacOSDesignModel
  @Environment(\.dismiss) private var dismiss
  @State private var selectedAccountIDs = Set<String>()

  private let selectionLimit = BackupLimits.maxAccounts

  private var savedAccounts: [DesignAccount] {
    model.accounts.filter { $0.kind == .saved }
  }

  var body: some View {
    DesignSheetHost(
      title: "Export Accounts",
      width: 480,
      primaryTitle: "Export",
      primaryDisabled: selectedAccountIDs.isEmpty,
      primaryIdentifier: "backup-create.confirm",
      onCancel: { dismiss() },
      onPrimary: {
        if model.createBackup(accountIDs: selectedAccountIDs) { dismiss() }
      }
    ) {
      Form {
        Section {
          if savedAccounts.isEmpty {
            DesignEmptyRow(text: "No saved accounts yet.")
          }
          ForEach(savedAccounts) { account in
            Toggle(
              isOn: Binding(
                get: { selectedAccountIDs.contains(account.id) },
                set: { on in
                  if on, selectedAccountIDs.count < selectionLimit {
                    selectedAccountIDs.insert(account.id)
                  } else if !on {
                    selectedAccountIDs.remove(account.id)
                  }
                }
              )
            ) {
              Label {
                VStack(alignment: .leading, spacing: 2) {
                  Text(account.name)
                  Text(account.detail)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                }
              } icon: {
                DesignProviderIcon(
                  provider: account.provider,
                  fallbackSymbol: account.symbol
                )
              }
            }
            .disabled(
              !selectedAccountIDs.contains(account.id)
                && selectedAccountIDs.count >= selectionLimit
            )
            .accessibilityIdentifier("backup-create.account.\(account.id)")
          }
        } header: {
          HStack {
            Text("Saved Accounts")
            Spacer()
            if savedAccounts.count > 1 {
              Button(selectedAccountIDs.count == savedAccounts.count ? "Deselect All" : "Select All") {
                if selectedAccountIDs.count == savedAccounts.count {
                  selectedAccountIDs.removeAll()
                } else {
                  selectedAccountIDs = Set(savedAccounts.prefix(selectionLimit).map(\.id))
                }
              }
              .buttonStyle(.plain)
              .font(.caption)
              .foregroundStyle(Color.accentColor)
            }
          }
        } footer: {
          if savedAccounts.isEmpty {
            Text("Add a login under Accounts & APIs, then choose accounts here.")
          } else {
            Text("These files include credentials and aren’t encrypted.")
          }
        }
      }
      .onAppear {
        selectedAccountIDs = Set(savedAccounts.prefix(selectionLimit).map(\.id))
      }
    }
  }
}

struct RestoreBackupSheet: View {
  @ObservedObject var model: MacOSDesignModel
  let backupID: String
  var importedPackage = false
  @Environment(\.dismiss) private var dismiss
  @State private var selectedAccountIDs = Set<String>()
  @State private var confirmDuplicates = false

  private var backup: DesignBackup? { model.backups.first { $0.id == backupID } }

  private var duplicateCount: Int {
    (backup?.accounts ?? []).filter {
      selectedAccountIDs.contains($0.id) && alreadySaved($0)
    }.count
  }

  var body: some View {
    DesignSheetHost(
      title: importedPackage ? "Import Accounts" : "Restore Accounts",
      width: 480,
      primaryTitle: primaryTitle,
      primaryDisabled: selectedAccountIDs.isEmpty || model.backupInspectionState == .importing,
      primaryIdentifier: "backup-restore.confirm",
      onCancel: { dismiss() },
      onPrimary: {
        if model.backupInspectionState == .success {
          dismiss()
          return
        }
        if model.backupInspectionState == .expired
          || model.backupInspectionState == .atomicFailure
        {
          if model.importBackupPackage() { dismiss() }
          return
        }
        if duplicateCount > 0 { confirmDuplicates = true }
        else { restore(overwrite: false) }
      }
    ) {
      Form {
        if model.backupInspectionState != .idle && model.backupInspectionState != .ready {
          restoreOutcome
        }
        if let backup {
          Section {
            ForEach(backup.accounts) { account in
              Toggle(
                isOn: Binding(
                  get: { selectedAccountIDs.contains(account.id) },
                  set: { on in
                    if on { selectedAccountIDs.insert(account.id) }
                    else { selectedAccountIDs.remove(account.id) }
                  }
                )
              ) {
                Label {
                  VStack(alignment: .leading, spacing: 2) {
                    HStack(spacing: 6) {
                      Text(account.name)
                      if alreadySaved(account) {
                        Text("Existing")
                          .font(.caption2)
                          .foregroundStyle(.secondary)
                          .accessibilityLabel("Already saved")
                      }
                    }
                    Text(account.detail)
                      .foregroundStyle(.secondary)
                      .lineLimit(1)
                  }
                } icon: {
                  DesignProviderIcon(
                    provider: account.provider,
                    fallbackSymbol: account.symbol
                  )
                }
              }
              .disabled(account.status == .unavailable)
              .accessibilityIdentifier("backup-restore.account.\(account.id)")
            }
          } header: {
            HStack {
              Text("Accounts in Backup")
              Spacer()
              let available = backup.accounts.filter { $0.status != .unavailable }
              if available.count > 1 {
                Button(selectedAccountIDs.count == available.count ? "Deselect All" : "Select All") {
                  if selectedAccountIDs.count == available.count {
                    selectedAccountIDs.removeAll()
                  } else {
                    selectedAccountIDs = Set(available.map(\.id))
                  }
                }
                .buttonStyle(.plain)
                .font(.caption)
                .foregroundStyle(Color.accentColor)
              }
            }
          } footer: {
            Text("Turn off an account to leave it out. Accounts already saved are skipped unless you choose Replace.")
          }
        }
      }
      .onAppear {
        selectedAccountIDs = Set(
          (backup?.accounts ?? []).filter { $0.status != .unavailable }.map(\.id)
        )
      }
      .confirmationDialog(
        "Replace Existing Accounts?",
        isPresented: $confirmDuplicates,
        titleVisibility: .visible
      ) {
        Button("Skip") { restore(overwrite: false) }
        Button("Replace", role: .destructive) { restore(overwrite: true) }
        Button("Cancel", role: .cancel) {}
      } message: {
        Text(
          duplicateCount == 1
            ? "1 selected account is already saved."
            : "\(duplicateCount) selected accounts are already saved."
        )
      }
    }
  }

  private func alreadySaved(_ account: DesignAccount) -> Bool {
    model.alreadySavedAccount(account)
  }

  private func restore(overwrite: Bool) {
    model.restoreBackup(id: backupID, accountIDs: selectedAccountIDs, overwrite: overwrite)
  }

  private var primaryTitle: String {
    switch model.backupInspectionState {
    case .success: "Done"
    case .expired, .atomicFailure: "Review Again"
    default: "Import"
    }
  }

  @ViewBuilder
  private var restoreOutcome: some View {
    switch model.backupInspectionState {
    case .importing:
      Section {
        HStack(spacing: 8) {
          ProgressView()
            .controlSize(.small)
          Text(model.backupInspectionMessage ?? "Importing Accounts…")
            .foregroundStyle(.secondary)
        }
      }
    case .success:
      Section {
        Label(model.backupInspectionMessage ?? "Accounts Imported", systemImage: "checkmark.circle.fill")
          .foregroundStyle(.green)
      }
    case .expired:
      restoreIssue(
        title: "Review Expired",
        message: model.backupInspectionMessage ?? "Review the backup again before importing."
      )
    case .atomicFailure:
      restoreIssue(
        title: "Couldn’t Import Accounts",
        message: model.backupInspectionMessage ?? "No saved accounts were changed."
      )
    default:
      EmptyView()
    }
  }

  private func restoreIssue(title: String, message: String) -> some View {
    Section {
      Label {
        VStack(alignment: .leading, spacing: 2) {
          Text(title)
          Text(message)
            .foregroundStyle(.secondary)
        }
      } icon: {
        DesignWarningSymbol()
      }
    }
  }
}
