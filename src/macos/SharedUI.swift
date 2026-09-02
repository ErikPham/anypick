import AppKit
import SwiftUI

extension View {
  /// Fixture `--appearance` only. `.system` must not call `preferredColorScheme`
  /// — passing `nil` still blocks the window from tracking macOS Light/Dark.
  @ViewBuilder
  func followingDesignAppearance(_ appearance: DesignAppearance) -> some View {
    if let scheme = appearance.colorScheme {
      preferredColorScheme(scheme)
    } else {
      self
    }
  }
}

@MainActor
func openDesignSettings() {
  if !NSApplication.shared.sendAction(Selector(("showSettingsWindow:")), to: nil, from: nil) {
    _ = NSApplication.shared.sendAction(Selector(("showPreferencesWindow:")), to: nil, from: nil)
  }
  NSApplication.shared.activate(ignoringOtherApps: true)
}

extension DesignApp {
  /// Copy shown when an app has no working login; `nil` keeps the row clean.
  var issue: String? {
    switch status {
    case .attention, .unavailable: "This app doesn’t have a working login."
    case .ready, .running, .stopped: nil
    }
  }
}

struct DesignStatusLabel: View {
  let status: DesignStatus

  var body: some View {
    Text(status.rawValue)
      .foregroundStyle(.secondary)
      .accessibilityLabel("Status: \(status.rawValue)")
  }
}

/// System warning glyph. Multicolor is the Battery / Wi‑Fi treatment —
/// do not paint `.yellow` over it.
struct DesignWarningSymbol: View {
  var scale: Image.Scale = .small

  var body: some View {
    Image(systemName: "exclamationmark.triangle.fill")
      .symbolRenderingMode(.multicolor)
      .imageScale(scale)
  }
}

/// Settings list identity: title, then a caption. Issues use the Battery
/// warning line — triangle + copy under the title, not beside the chevron.
struct DesignIdentityRow<Icon: View, Trailing: View>: View {
  let title: String
  let detail: String
  var issue: String? = nil
  @ViewBuilder var icon: () -> Icon
  @ViewBuilder var trailing: () -> Trailing

  var body: some View {
    HStack(spacing: 12) {
      Label {
        VStack(alignment: .leading, spacing: 2) {
          Text(title)
          if !detail.isEmpty {
            Text(detail)
              .font(.subheadline)
              .foregroundStyle(.secondary)
              .lineLimit(1)
          }
          if let issue {
            HStack(alignment: .firstTextBaseline, spacing: 4) {
              DesignWarningSymbol()
              Text(issue)
                .fixedSize(horizontal: false, vertical: true)
            }
            .font(.subheadline)
            .foregroundStyle(.secondary)
            .accessibilityElement(children: .combine)
            .accessibilityLabel(issue)
          }
        }
        // Accounts & APIs' inventory row sizes its text block with
        // `maxWidth: .infinity` and pins the trailing cluster with
        // `.fixedSize()`. A bare `Spacer` only expands when something above it
        // already promised the width, so rows built on this helper stayed at
        // their intrinsic width and did not reach the right edge when the
        // window widened — the same content looked narrower here than in
        // Accounts. Match that row's geometry so every destination fills alike.
        .frame(maxWidth: .infinity, alignment: .leading)
      } icon: {
        icon()
      }
      Spacer(minLength: 8)
      trailing()
        .fixedSize()
    }
  }
}

extension DesignIdentityRow where Trailing == EmptyView {
  init(title: String, detail: String, issue: String? = nil, @ViewBuilder icon: @escaping () -> Icon) {
    self.init(title: title, detail: detail, issue: issue, icon: icon, trailing: { EmptyView() })
  }
}

struct DesignEmptyState: View {
  let title: String
  let message: String
  let symbol: String
  var actionTitle: String?
  var action: (() -> Void)?

  var body: some View {
    ContentUnavailableView {
      Label(title, systemImage: symbol)
    } description: {
      Text(message)
    } actions: {
      if let actionTitle, let action {
        Button(actionTitle, action: action)
      }
    }
  }
}

struct DesignLoadingState: View {
  var body: some View {
    ProgressView("Loading AnyPick…")
      .controlSize(.large)
      .frame(maxWidth: .infinity, maxHeight: .infinity)
      .accessibilityIdentifier("state.loading")
  }
}

/// Grouped Form, then loose buttons under the last group — Displays
/// “Advanced…” / “Night Shift…”. Not a Form section, not the toolbar.
struct DesignGroupedPane<Content: View, Actions: View>: View {
  @ViewBuilder var content: () -> Content
  @ViewBuilder var actions: () -> Actions
  private var showsActions: Bool

  init(@ViewBuilder content: @escaping () -> Content) where Actions == EmptyView {
    self.content = content
    self.actions = { EmptyView() }
    self.showsActions = false
  }

  init(
    @ViewBuilder content: @escaping () -> Content,
    @ViewBuilder actions: @escaping () -> Actions
  ) {
    self.content = content
    self.actions = actions
    self.showsActions = true
  }

  var body: some View {
    // Every pane renders through this one wrapper. Splitting it — a bare Form
    // when there are no action buttons, a ScrollView+VStack when there are —
    // gave panes without actions a different width, scrolling behaviour and
    // vertical padding than panes with them, so the same content looked
    // different from one destination to the next.
    ScrollView {
      VStack(alignment: .trailing, spacing: 12) {
        Form {
          content()
        }
        .formStyle(.grouped)
        // `fixedSize(vertical:)` keeps the Form at its natural height so the
        // ScrollView is what scrolls. Left alone it also let the Form keep its
        // *natural width*, which differs per destination — Accounts & APIs'
        // content measured wider than Overview's, so NavigationSplitView
        // resolved a different column width per destination and the sidebar
        // appeared to change shape when you switched menus. Pin the width to
        // the available space and only the height stays intrinsic.
        .fixedSize(horizontal: false, vertical: true)
        .frame(maxWidth: .infinity)

        if showsActions {
          HStack(spacing: 12) {
            actions()
          }
          .buttonStyle(.bordered)
          .controlSize(.regular)
          .padding(.trailing, 20)
          .padding(.bottom, 20)
        }
      }
      .frame(maxWidth: .infinity, alignment: .trailing)
    }
  }
}

/// AppKit push buttons — Settings sheets, not SwiftUI `.bordered`.
struct DesignSheetButton: NSViewRepresentable {
  var title: String
  var keyEquivalent: String = ""
  var enabled = true
  var destructive = false
  var identifier: String? = nil
  var action: () -> Void

  func makeCoordinator() -> Coordinator {
    Coordinator(action: action)
  }

  func makeNSView(context: Context) -> NSButton {
    let button = NSButton(
      title: title,
      target: context.coordinator,
      action: #selector(Coordinator.click)
    )
    button.bezelStyle = .rounded
    button.controlSize = .regular
    button.setButtonType(.momentaryPushIn)
    button.keyEquivalent = keyEquivalent
    button.isEnabled = enabled
    button.hasDestructiveAction = destructive
    button.bezelColor = destructive ? .systemRed : nil
    button.contentTintColor = destructive ? .white : nil
    updateTitle(of: button)
    if let identifier {
      button.setAccessibilityIdentifier(identifier)
    }
    button.setContentHuggingPriority(.required, for: .horizontal)
    button.setContentCompressionResistancePriority(.required, for: .horizontal)
    return button
  }

  func updateNSView(_ button: NSButton, context: Context) {
    button.keyEquivalent = keyEquivalent
    button.isEnabled = enabled
    button.hasDestructiveAction = destructive
    button.bezelColor = destructive ? .systemRed : nil
    button.contentTintColor = destructive ? .white : nil
    updateTitle(of: button)
    if let identifier {
      button.setAccessibilityIdentifier(identifier)
    }
    context.coordinator.action = action
  }

  private func updateTitle(of button: NSButton) {
    guard destructive else {
      if button.title != title { button.title = title }
      return
    }
    button.attributedTitle = NSAttributedString(
      string: title,
      attributes: [
        .font: button.font ?? NSFont.systemFont(ofSize: NSFont.systemFontSize),
        .foregroundColor: NSColor.white,
      ]
    )
  }

  final class Coordinator: NSObject {
    var action: () -> Void
    init(action: @escaping () -> Void) { self.action = action }
    @objc func click() { action() }
  }
}

/// Sheet: Form group, then Cancel + default under that group.
struct DesignSheetHost<Content: View>: View {
  let title: String
  var width: CGFloat = 480
  var height: CGFloat = 440
  var primaryTitle: String
  var primaryDisabled = false
  var primaryIdentifier: String?
  var primaryRole: ButtonRole? = nil
  var showsCancel = true
  var onCancel: () -> Void
  var onPrimary: () -> Void
  @ViewBuilder var content: () -> Content

  var body: some View {
    VStack(alignment: .trailing, spacing: 12) {
      content()
        .formStyle(.grouped)
        .frame(maxWidth: .infinity, maxHeight: .infinity)

      HStack(spacing: 12) {
        if showsCancel {
          DesignSheetButton(title: "Cancel", keyEquivalent: "\u{1b}", action: onCancel)
            .fixedSize()
        }
        DesignSheetButton(
          title: primaryTitle,
          keyEquivalent: "\r",
          enabled: !primaryDisabled,
          destructive: primaryRole == .destructive,
          identifier: primaryIdentifier ?? "sheet.primary",
          action: onPrimary
        )
        .fixedSize()
      }
      .padding(.trailing, 20)
      .padding(.bottom, 16)
    }
    .background(Color(nsColor: .windowBackgroundColor))
    .frame(width: width)
    .frame(minHeight: 280, idealHeight: height, maxHeight: 640)
  }
}
