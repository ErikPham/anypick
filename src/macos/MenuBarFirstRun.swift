import SwiftUI

struct MenuBarFirstRun: View {
  @ObservedObject var model: MacOSDesignModel
  var openAccounts: () -> Void

  var body: some View {
    VStack(spacing: 9) {
      VStack(spacing: 6) {
        ZStack {
          RoundedRectangle(cornerRadius: 12, style: .continuous)
            .fill(TrayPaint.accent)
            .frame(width: 44, height: 44)
          Image(systemName: "sparkle")
            .font(.system(size: 18, weight: .semibold))
            .foregroundStyle(.white)
        }
        Text(model.currentLiveLogin == nil ? "Add a login to start" : "Save a login to start")
          .font(.system(size: 17, weight: .semibold))
        Text(firstRunDescription)
          .font(.system(size: 11))
          .foregroundStyle(.secondary)
      }
      .padding(.top, 4)
      .padding(.horizontal, 8)

      if let login = model.currentLiveLogin {
        HStack(spacing: 10) {
          DesignProviderIcon(provider: login.providerID, fallbackSymbol: login.symbol, size: 32)
          VStack(alignment: .leading, spacing: 2) {
            Text(login.appName)
              .font(.system(size: 12, weight: .semibold))
            Text(login.identity ?? "Current login detected")
              .font(TrayFont.detail)
              .foregroundStyle(.secondary)
          }
          Spacer(minLength: 6)
          HStack(spacing: 4) {
            Circle().fill(TrayPaint.ready).frame(width: 6, height: 6)
            Text("Active")
              .font(TrayFont.detailStrong)
              .foregroundStyle(TrayPaint.ready)
          }
          .padding(.horizontal, 6)
          .frame(height: 20)
          .background(
            TrayPaint.ready.opacity(0.12),
            in: RoundedRectangle(cornerRadius: 6, style: .continuous)
          )
        }
        .padding(10)
        .frame(height: 62)
        .background(TrayPaint.cardFill, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
        .overlay(
          RoundedRectangle(cornerRadius: 10, style: .continuous)
            .strokeBorder(TrayPaint.hairline, lineWidth: 0.5)
        )
      }

      if let login = model.currentLiveLogin {
        VStack(alignment: .leading, spacing: 7) {
          trustRow("Keeps the current \(login.appName) session signed in")
          trustRow("Stores an encrypted local snapshot for switching")
          trustRow("You can remove or replace it at any time")
        }
        .padding(10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(
          TrayPaint.accent.opacity(0.12),
          in: RoundedRectangle(cornerRadius: 10, style: .continuous)
        )
      }

      Button(action: saveDetected) {
        HStack(spacing: 7) {
          if model.isBusy {
            ProgressView().controlSize(.small)
          } else {
            Image(systemName: model.currentLiveLogin == nil ? "person.badge.plus" : "square.and.arrow.down")
          }
          Text(model.currentLiveLogin == nil ? "Add Login…" : "Save This Login")
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

      HStack(spacing: 8) {
        stepBadge(1, current: true)
        Text(model.currentLiveLogin == nil ? "Add login" : "Save login")
          .font(TrayFont.detailStrong)
        Rectangle()
          .fill(Color.primary.opacity(0.18))
          .frame(height: 1)
        stepBadge(2, current: false)
        Text("Point an app")
          .font(TrayFont.detailStrong)
          .foregroundStyle(.secondary)
      }
      .padding(.top, 3)
      .padding(.horizontal, 2)
    }
  }

  private func trustRow(_ text: String) -> some View {
    HStack(alignment: .center, spacing: 7) {
      Image(systemName: "checkmark.circle")
        .font(.system(size: 12, weight: .medium))
        .foregroundStyle(TrayPaint.accent)
      Text(text)
        .font(TrayFont.detailMedium)
        .foregroundStyle(TrayPaint.accentInk)
        .fixedSize(horizontal: false, vertical: true)
    }
    .frame(height: 19, alignment: .leading)
  }

  private func stepBadge(_ number: Int, current: Bool) -> some View {
    Text("\(number)")
      .font(TrayFont.detailStrong)
      .foregroundStyle(current ? Color.white : Color.secondary)
      .frame(width: 18, height: 18)
      .background(
        current ? Color.primary : Color.primary.opacity(0.15),
        in: Circle()
      )
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
