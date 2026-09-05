import SwiftUI

/// Design-fixture desktop so board 29 is visible without hunting the status item.
struct MenuBarPreview: View {
  @ObservedObject var model: MacOSDesignModel
  @Environment(\.colorScheme) private var colorScheme

  var body: some View {
    ZStack(alignment: .topTrailing) {
      desktopBackground
      MenuBarMenu(model: model)
        .background(.regularMaterial)
        .border(Color.primary.opacity(0.1))
        .shadow(color: Color.black.opacity(0.19), radius: 18, y: 10)
        .padding(18)
    }
    .frame(minWidth: 420, minHeight: 680)
    .followingDesignAppearance(model.appearance)
  }

  @ViewBuilder
  private var desktopBackground: some View {
    if colorScheme == .dark {
      Color(nsColor: .underPageBackgroundColor)
    } else {
      Color(nsColor: .windowBackgroundColor)
    }
  }
}
