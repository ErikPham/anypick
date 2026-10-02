import SwiftUI

@MainActor
func trayInlineSwitchState(
  model: MacOSDesignModel,
  app: DesignApp?,
  route: DesignRoute
) -> TrayInlineSwitchState {
  guard let app else { return .available }
  // Names collide across accounts. The phase carries the route id for this.
  return traySwitchStateForRoute(
    phase: model.switchPhase,
    appID: app.id,
    routeID: route.id,
    selectedRouteID: app.selectedRouteID)
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
