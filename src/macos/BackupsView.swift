import SwiftUI

private struct BackupPresentation: Identifiable {
  let id: String
  let importedPackage: Bool
}

struct BackupsView: View {
  @ObservedObject var model: MacOSDesignModel
  @State private var showingCreate = false
  @State private var backupPresentation: BackupPresentation?
  @State private var pendingForgetBackup: DesignBackup?

  init(model: MacOSDesignModel) {
    self.model = model
    _showingCreate = State(initialValue: model.pendingStartupFlow == "create-backup")
    if model.pendingStartupFlow == "import-backup" {
      let id = model.backups.first(where: { $0.id == "backup-import" })?.id
        ?? model.backups.last?.id
      _backupPresentation = State(
        initialValue: id.map { BackupPresentation(id: $0, importedPackage: true) }
      )
    } else if model.pendingStartupFlow == "restore-backup", let id = model.backups.first?.id {
      _backupPresentation = State(initialValue: BackupPresentation(id: id, importedPackage: false))
    } else {
      _backupPresentation = State(initialValue: nil)
    }
  }

  var body: some View {
    DesignGroupedPane {
      backupInspectionSection

      Section {
        Label {
          VStack(alignment: .leading, spacing: 2) {
            Text("Backup files contain working credentials")
            Text("They are readable JSON and are not encrypted. Store them securely and don’t share them.")
              .foregroundStyle(.secondary)
          }
        } icon: {
          DesignWarningSymbol(scale: .medium)
        }
      }

      Section {
        LabeledContent {
          Button("Choose Accounts…") { showingCreate = true }
            .disabled(model.accounts.first(where: { $0.kind == .saved }) == nil)
            .accessibilityIdentifier("backups.choose-accounts")
        } label: {
          Label {
            VStack(alignment: .leading, spacing: 2) {
              Text("Create an account backup")
              Text("Choose saved accounts and a destination.")
                .foregroundStyle(.secondary)
            }
          } icon: {
            Image(systemName: "square.and.arrow.up")
          }
        }
      } header: {
        Text("Export Accounts")
      }

      Section {
        LabeledContent {
          Button("Choose Backup…", action: chooseBackup)
            .accessibilityIdentifier("backups.import")
        } label: {
          Label {
            VStack(alignment: .leading, spacing: 2) {
              Text("Review an account backup")
              Text("Nothing changes until you review and confirm each account.")
                .foregroundStyle(.secondary)
            }
          } icon: {
            Image(systemName: "square.and.arrow.down")
          }
        }
      } header: {
        Text("Import Accounts")
      } footer: {
        Text("Saved accounts only. API gateways, app settings, activity, and AnyPick settings are not included.")
      }

      if !model.backups.isEmpty {
        Section {
          ForEach(model.backups) { backup in
            LabeledContent {
              HStack {
                Button("Review") {
                  backupPresentation = BackupPresentation(id: backup.id, importedPackage: true)
                }
                Button("Forget Review…", role: .destructive) {
                  pendingForgetBackup = backup
                }
              }
            } label: {
              Label {
                VStack(alignment: .leading, spacing: 2) {
                  Text(backup.createdAt)
                  Text("\(backup.accountCount) accounts · \(backup.location)")
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                }
              } icon: {
                Image(systemName: "externaldrive.badge.checkmark")
              }
            }
            .accessibilityIdentifier("backups.inventory.\(backup.id)")
          }
        } header: {
          Text("Reviewed Backups")
        } footer: {
          Text("For imported files, AnyPick keeps only a protected staging copy until you forget the review.")
        }
      }
    }
    .sheet(isPresented: $showingCreate) {
      CreateBackupSheet(model: model)
    }
    .sheet(item: $backupPresentation) { presentation in
      RestoreBackupSheet(
        model: model,
        backupID: presentation.id,
        importedPackage: presentation.importedPackage
      )
    }
    .onChange(of: model.pendingImportBackupID) { _, id in
      if id != nil { presentImportedBackup() }
    }
    .confirmationDialog(
      "Forget This Backup Review?",
      isPresented: Binding(
        get: { pendingForgetBackup != nil },
        set: { if !$0 { pendingForgetBackup = nil } }
      ),
      titleVisibility: .visible
    ) {
      Button("Forget Review", role: .destructive) {
        guard let backup = pendingForgetBackup else { return }
        model.deleteBackup(id: backup.id)
        pendingForgetBackup = nil
      }
      Button("Cancel", role: .cancel) {}
    } message: {
      Text("This discards AnyPick’s protected review copy. The original backup file is not deleted.")
    }
  }

  private func chooseBackup() {
    if model.importBackupPackage() { presentImportedBackup() }
  }

  private func presentImportedBackup() {
    guard let id = model.pendingImportBackupID else { return }
    backupPresentation = BackupPresentation(id: id, importedPackage: true)
    model.pendingImportBackupID = nil
  }

  @ViewBuilder
  private var backupInspectionSection: some View {
    switch model.backupInspectionState {
    case .idle, .ready:
      EmptyView()
    case .reading, .importing:
      Section {
        HStack(spacing: 8) {
          ProgressView()
            .controlSize(.small)
          Text(model.backupInspectionMessage ?? "Reading Backup…")
            .foregroundStyle(.secondary)
        }
        .accessibilityIdentifier("backups.inspection.\(model.backupInspectionState.rawValue)")
      }
    case .damaged:
      backupIssue(
        title: "Backup Is Damaged or Incomplete",
        message: model.backupInspectionMessage ?? "Nothing was imported.",
        actionTitle: "Choose Another File…",
        action: chooseBackup
      )
    case .newerVersion:
      backupIssue(
        title: "Created by a Newer AnyPick Version",
        message: model.backupInspectionMessage ?? "Update AnyPick, then review the file again.",
        actionTitle: "Choose Another File…",
        action: chooseBackup
      )
    case .permissionDenied:
      backupIssue(
        title: "Can’t Read This File",
        message: model.backupInspectionMessage ?? "AnyPick does not have permission to open it.",
        actionTitle: "Choose Another File…",
        action: chooseBackup
      )
    case .expired:
      backupIssue(
        title: "Review Expired",
        message: model.backupInspectionMessage ?? "Review the backup again before importing.",
        actionTitle: "Review Again…",
        action: chooseBackup
      )
    case .success:
      backupResult(
        title: model.backupInspectionMessage ?? "Accounts Imported",
        message: "Only the accounts selected in the protected review copy were changed.",
        symbol: "checkmark.circle.fill"
      )
    case .atomicFailure:
      backupIssue(
        title: "Couldn’t Import Accounts",
        message: model.backupInspectionMessage ?? "No saved accounts were changed.",
        actionTitle: "Review Again…",
        action: chooseBackup
      )
    }
  }

  private func backupIssue(
    title: String,
    message: String,
    actionTitle: String,
    action: @escaping () -> Void
  ) -> some View {
    Section {
      LabeledContent {
        Button(actionTitle, action: action)
      } label: {
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
      .accessibilityIdentifier("backups.inspection.\(model.backupInspectionState.rawValue)")
    }
  }

  private func backupResult(title: String, message: String, symbol: String) -> some View {
    Section {
      Label {
        VStack(alignment: .leading, spacing: 2) {
          Text(title)
          Text(message)
            .foregroundStyle(.secondary)
        }
      } icon: {
        Image(systemName: symbol)
          .foregroundStyle(.green)
      }
    }
  }
}
