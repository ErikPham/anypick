import SwiftUI

struct MenuBarFirstRun: View {
  @ObservedObject var model: MacOSDesignModel
  var openAccounts: () -> Void

  var body: some View {
    VStack(alignment: .leading, spacing: 8) {
      VStack(alignment: .leading, spacing: 4) {
        Label {
          Text(model.currentLiveLogin == nil ? "Add a login to start" : "Save a login to start")
            .font(.headline)
        } icon: {
          Image(systemName: "sparkle")
            .foregroundStyle(Color.accentColor)
        }
        Text(firstRunDescription)
          .font(.caption)
          .foregroundStyle(.secondary)
          .fixedSize(horizontal: false, vertical: true)
      }

      if let login = model.currentLiveLogin {
        HStack(spacing: 8) {
          DesignProviderIcon(provider: login.providerID, fallbackSymbol: login.symbol, size: 28)
            .accessibilityHidden(true)
          VStack(alignment: .leading, spacing: 1) {
            Text(login.appName)
              .font(.callout)
              .fontWeight(.semibold)
              .lineLimit(1)
            Text(login.identity ?? "Current login detected")
              .font(.caption)
              .foregroundStyle(.secondary)
              .lineLimit(1)
          }
          .frame(maxWidth: .infinity, alignment: .leading)
          Spacer(minLength: 6)
          Label("Active", systemImage: "checkmark.circle.fill")
            .font(.caption)
            .fontWeight(.semibold)
            .foregroundStyle(.green)
        }
        .padding(.vertical, 6)
        Divider()
        VStack(alignment: .leading, spacing: 4) {
          trustRow("Keeps the current \(login.appName) session signed in")
          trustRow("Stores an encrypted local snapshot for switching")
          trustRow("You can remove or replace it at any time")
        }
      }

      Button(action: saveDetected) {
        HStack(spacing: 6) {
          if model.isBusy {
            ProgressView().controlSize(.small)
          } else {
            Image(systemName: model.currentLiveLogin == nil ? "person.badge.plus" : "square.and.arrow.down")
          }
          Text(model.currentLiveLogin == nil ? "Open Accounts" : "Save This Login")
        }
        .frame(maxWidth: .infinity)
      }
      .buttonStyle(.borderedProminent)
      .controlSize(.large)
      .disabled(model.isBusy)
      .accessibilityIdentifier("tray.first-run.save")

      if model.currentLiveLogin != nil {
        Button(action: openAccounts) {
          Text("Choose Another Login…")
            .frame(maxWidth: .infinity)
        }
        .buttonStyle(.bordered)
        .controlSize(.large)
      }

      HStack(spacing: 6) {
        Text("1")
          .font(.caption)
          .fontWeight(.semibold)
          .foregroundStyle(.white)
          .frame(width: 18, height: 18)
          .background(Color.accentColor, in: Circle())
          .accessibilityHidden(true)
        Text(model.currentLiveLogin == nil ? "Add login" : "Save login")
          .font(.caption)
          .fontWeight(.semibold)
        Rectangle()
          .fill(Color.secondary.opacity(0.3))
          .frame(height: 1)
          .accessibilityHidden(true)
        Text("2")
          .font(.caption)
          .fontWeight(.semibold)
          .foregroundStyle(.secondary)
          .frame(width: 18, height: 18)
          .background(Color.secondary.opacity(0.15), in: Circle())
          .accessibilityHidden(true)
        Text("Point an app")
          .font(.caption)
          .fontWeight(.semibold)
          .foregroundStyle(.secondary)
      }
      .accessibilityElement(children: .combine)
      .accessibilityLabel("Step 1 of 2: \(model.currentLiveLogin == nil ? "Add login" : "Save login"). Step 2: Point an app.")
    }
  }

  private func trustRow(_ text: String) -> some View {
    Label {
      Text(text)
        .font(.caption)
        .fixedSize(horizontal: false, vertical: true)
    } icon: {
      Image(systemName: "checkmark.circle")
        .foregroundStyle(Color.accentColor)
    }
  }

  private func saveDetected() {
    guard let login = model.currentLiveLogin else {
      openAccounts()
      return
    }
    _ = model.saveCurrentLogin(login)
  }

  private var firstRunDescription: String {
    guard let login = model.currentLiveLogin else {
      return "Sign in with a supported app, then save it in AnyPick."
    }
    return "We found an active \(login.appName) login."
  }
}
