import SwiftUI

enum TrayInlineSwitchState {
  case available
  case current
  case switching
  case retry
}

@MainActor
func trayInlineSwitchState(
  model: MacOSDesignModel,
  app: DesignApp?,
  route: DesignRoute
) -> TrayInlineSwitchState {
  guard let app else { return .available }
  if route.id == app.selectedRouteID { return .current }

  switch model.switchPhase {
  case let .inFlight(appID, _, target) where appID == app.id && route.name == target:
    return .switching
  case let .recovery(appID, requested, _) where appID == app.id && route.name == requested:
    return .retry
  default:
    return .available
  }
}

struct TrayInlineRouteMark: View {
  let state: TrayInlineSwitchState

  var body: some View {
    Group {
      switch state {
      case .current:
        Image(systemName: "checkmark.circle.fill")
          .foregroundStyle(Color.accentColor)
      case .switching:
        ProgressView().controlSize(.small)
      case .retry:
        Image(systemName: "exclamationmark.triangle.fill")
          .foregroundStyle(.orange)
      case .available:
        Image(systemName: "circle")
          .foregroundStyle(.secondary)
      }
    }
    .frame(width: 18, height: 18)
  }
}
