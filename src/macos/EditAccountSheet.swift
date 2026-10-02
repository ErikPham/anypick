import SwiftUI

struct EditAccountSheet: View {
  @ObservedObject var model: MacOSDesignModel
  let accountID: String
  @Environment(\.dismiss) private var dismiss
  @State private var name = ""
  /// Production edit waits for the engine result like the add sheet.
  @State private var awaitingSaveResult = false
  @State private var saveError: String?
  @FocusState private var nameFocused: Bool

  private var account: DesignAccount? { model.accounts.first { $0.id == accountID } }

  private var canSave: Bool {
    let cleaned = name.trimmingCharacters(in: .whitespacesAndNewlines)
    return !cleaned.isEmpty && cleaned != account?.name
  }

  var body: some View {
    DesignSheetHost(
      title: "Edit Account",
      width: 440,
      primaryTitle: "Save",
      primaryDisabled: !canSave || model.isBusy || awaitingSaveResult,
      primaryIdentifier: "account-edit.save",
      onCancel: { dismiss() },
      onPrimary: save
    ) {
      Form {
        if let account {
          Section {
            HStack(spacing: 12) {
              DesignProviderIcon(
                provider: account.provider,
                fallbackSymbol: account.symbol,
                size: 30
              )
              VStack(alignment: .leading, spacing: 3) {
                Text(disambiguatedAccountName(for: account, in: model.accounts))
                  .font(.headline)
                Text(account.detail.isEmpty ? "Identity not reported" : account.detail)
                  .foregroundStyle(.secondary)
                  .lineLimit(1)
              }
              Spacer(minLength: 0)
            }
          } header: {
            Text("Account")
          }

          Section {
            TextField("Name", text: $name, prompt: Text("Work"))
              .focused($nameFocused)
              .accessibilityIdentifier("account-edit.name")
            LabeledContent(account.kind == .gateway ? "Endpoint" : "Email or ID") {
              Text(account.detail.isEmpty ? "Not reported" : account.detail)
                .foregroundStyle(.secondary)
                .textSelection(.enabled)
            }
            LabeledContent("Provider", value: account.provider)
            LabeledContent(
              "Type",
              value: account.kind == .gateway ? "API Gateway" : "Saved Login"
            )
            LabeledContent("Status") {
              if model.isCurrentAccount(account) {
                Label("Current", systemImage: "checkmark.circle.fill")
                  .foregroundStyle(.green)
              } else {
                Label(account.status.rawValue, systemImage: account.status.symbol)
                  .foregroundStyle(account.status.color)
              }
            }
          } header: {
            Text("Details")
          } footer: {
            Text("The name is shown in AnyPick. The provider login and identity are unchanged.")
          }

          if let saveError {
            Section {
              Label {
                Text(saveError)
                  .font(.subheadline)
                  .foregroundStyle(.secondary)
                  .fixedSize(horizontal: false, vertical: true)
              } icon: {
                Image(systemName: "exclamationmark.triangle.fill")
                  .foregroundStyle(.orange)
              }
            }
            .accessibilityIdentifier("account-edit.error")
          }
        } else {
          ContentUnavailableView(
            "Account Unavailable",
            systemImage: "person.crop.circle.badge.exclamationmark",
            description: Text("This account is no longer available.")
          )
        }
      }
      .onAppear {
        name = account?.name ?? ""
        nameFocused = true
      }
      .onChange(of: model.operationResult) { _, result in
        guard awaitingSaveResult,
          result?.operation == "account-edit" || result?.operation == "gateway-edit"
        else { return }
        awaitingSaveResult = false
        if result?.succeeded == true {
          dismiss()
        } else if let message = result?.message, !message.isEmpty {
          saveError = displaySafeFeedback(
            message,
            fallback: "The account name could not be saved safely. Try again."
          )
        } else {
          saveError = "Couldn’t save that name. Try again."
        }
      }
    }
  }

  private func save() {
    saveError = nil
    if model.isProduction {
      // No mutation fires for a vanished account — never strand the sheet
      // waiting for a result that cannot arrive.
      guard model.accounts.contains(where: { $0.id == accountID }) else {
        saveError = "That account is no longer available."
        return
      }
      // Only arm the wait after the request is accepted; otherwise the sheet
      // would wait for a result that cannot arrive.
      guard !model.isBusy else {
        saveError = "Another change is in progress. Try again in a moment."
        return
      }
      if model.updateAccount(id: accountID, label: name) {
        awaitingSaveResult = true
      } else {
        saveError = "Another change is in progress. Try again in a moment."
      }
      return
    }
    model.updateAccount(id: accountID, label: name)
    dismiss()
  }
}
