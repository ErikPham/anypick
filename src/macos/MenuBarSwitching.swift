import SwiftUI

// NOTE (tray reset): enum lives in DesignDisplay.swift now; the old file's
// copy was removed to avoid a duplicate. Patterns below are widened to the
// current 4-tuple phase purely to compile — old name-matching behavior kept.

@MainActor
func trayInlineSwitchState(
  model: MacOSDesignModel,
  app: DesignApp?,
  route: DesignRoute
) -> TrayInlineSwitchState {
  guard let app else { return .available }
  if route.id == app.selectedRouteID { return .current }

  switch model.switchPhase {
  case let .inFlight(appID, _, target, _) where appID == app.id && route.name == target:
    return .switching
  case let .recovery(appID, requested, _, _) where appID == app.id && route.name == requested:
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
          .foregroundStyle(TrayPaint.accent)
      case .switching:
        ProgressView().controlSize(.small)
      case .retry:
        Image(systemName: "exclamationmark.triangle.fill")
          .foregroundStyle(TrayPaint.attention)
      case .available:
        Image(systemName: "circle")
          .foregroundStyle(.secondary)
      }
    }
    .frame(width: 18, height: 18)
  }
}
