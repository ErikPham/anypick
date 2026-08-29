import SwiftUI

/// Design-fixture desktop so board 29 is visible without hunting the status item.
struct MenuBarPreview: View {
  @ObservedObject var model: MacOSDesignModel
  @Environment(\.colorScheme) private var colorScheme

  var body: some View {
    ZStack(alignment: .topTrailing) {
      desktopBackground
      MenuBarMenu(model: model)
        .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
        .overlay(
          RoundedRectangle(cornerRadius: 14, style: .continuous)
            .strokeBorder(Color.primary.opacity(0.10), lineWidth: 0.5)
        )
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
      LinearGradient(
        colors: [
          Color(red: 0.86, green: 0.90, blue: 0.96),
          Color(red: 0.94, green: 0.89, blue: 0.93),
          Color(red: 0.85, green: 0.92, blue: 0.94),
        ],
        startPoint: .topLeading,
        endPoint: .bottomTrailing
      )
    }
  }
}
