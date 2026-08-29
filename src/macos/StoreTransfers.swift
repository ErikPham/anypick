import Foundation

// Module: StoreTransfers.swift — account import/export/backup commands

extension TrayStore {
  @discardableResult
  func transferAccount(
    operation: String,
    path: String,
    providerId: String? = nil,
    name: String? = nil,
    overwrite: Bool? = nil,
    activityLabel: String
  ) -> String? {
    let mutation = Mutation(
      requestId: UUID().uuidString,
      operation: operation,
      providerId: providerId,
      sourceId: nil,
      methodId: nil,
      name: name,
      path: path,
      label: nil,
      endpoint: nil,
      apiKey: nil,
      region: nil,
      defaultModel: nil,
      overwrite: overwrite,
      enabled: nil
    )
    return sendMutation(mutation, activityLabel: activityLabel)
  }

  @discardableResult
  func exportBackup(
    path: String,
    accounts: [BackupAccountRef],
    overwrite: Bool,
    activityLabel: String
  ) -> String? {
    let requestId = UUID().uuidString
    let mutation = BackupExportMutation(
      requestId: requestId,
      path: path,
      accounts: accounts,
      overwrite: overwrite
    )
    return sendMutationCommand(mutation, requestId: requestId, activityLabel: activityLabel)
  }

  @discardableResult
  func importBackup(
    inspectionId: String,
    entryIds: [String],
    overwriteConfirmed: Bool,
    activityLabel: String
  ) -> String? {
    let requestId = UUID().uuidString
    let mutation = BackupImportMutation(
      requestId: requestId,
      inspectionId: inspectionId,
      entryIds: entryIds,
      overwriteConfirmed: overwriteConfirmed
    )
    return sendMutationCommand(mutation, requestId: requestId, activityLabel: activityLabel)
  }
}
