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
    if !isAppRouteConfigured(self) {
      return "No login chosen. Open the app to choose one."
    }
    switch status {
    case .attention, .unavailable:
      return "This app doesn’t have a working login."
    case .ready, .running, .stopped:
      return nil
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
    .frame(maxWidth: .infinity, maxHeight: .infinity)
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

/// Grouped Form shared by every pane. Pane actions live in the last
/// section's `footer:` (see `DesignPaneActions`) — never as a Form section
/// of their own (a section always draws its bezel around the row) and never
/// in the toolbar.
///
/// Action controls must be `DesignSheetButton` (NSButton). SwiftUI `Button`
/// under `.searchable` is copied into the window toolbar on macOS 26.
struct DesignGroupedPane<Content: View>: View {
  @ViewBuilder var content: () -> Content
  /// When true the Form is allowed to grow to the full height of the detail
  /// column instead of sizing to its content. Used by panes whose body should
  /// fill the window (e.g. Activity's log/events well). Defaults to false so
  /// every other pane keeps the original top-aligned, content-sized layout.
  var fillHeight: Bool = false

  init(fillHeight: Bool = false, @ViewBuilder content: @escaping () -> Content) {
    self.fillHeight = fillHeight
    self.content = content
  }

  var body: some View {
    if fillHeight {
      // Fill the detail column: the Form takes the offered height and the
      // body's flexible children (e.g. Activity's log/events well, which
      // scroll internally) absorb the slack. No outer ScrollView — it would
      // propose an unbounded height and defeat the fill.
      Form {
        content()
      }
      .formStyle(.grouped)
      .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
    } else {
      ScrollView {
        Form {
          content()
        }
        .formStyle(.grouped)
        .fixedSize(horizontal: false, vertical: true)
        .frame(maxWidth: .infinity)
      }
      .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
    }
  }
}

/// Trailing action row for a section footer — Displays “Shortcuts…” under
/// the last group: one line, pinned to the section's own trailing edge by
/// the Form itself, so no hand-set inset is needed for alignment and the
/// vertical rhythm is the system's own footer spacing.
struct DesignPaneActions<Content: View>: View {
  @ViewBuilder var content: () -> Content

  var body: some View {
    HStack(spacing: 12) {
      Spacer(minLength: 0)
      content()
    }
    // Optical nudge: the system footer gap sits tighter than the Settings
    // action-row rhythm (Shortcuts… / Hot Corners…).
    .padding(.top, 6)
  }
}

/// Native toolbar search. `.searchable` on macOS 26 lifts descendant SwiftUI
/// `Button`s into the titlebar, which is how Add Account appeared twice.
struct DesignToolbarSearch: NSViewRepresentable {
  @Binding var text: String
  var prompt: String

  func makeCoordinator() -> Coordinator { Coordinator(text: $text) }

  func makeNSView(context: Context) -> NSSearchField {
    let field = NSSearchField()
    field.placeholderString = prompt
    field.delegate = context.coordinator
    field.target = context.coordinator
    field.action = #selector(Coordinator.changed(_:))
    field.sendsSearchStringImmediately = true
    field.sendsWholeSearchString = false
    field.controlSize = .regular
    return field
  }

  func updateNSView(_ field: NSSearchField, context: Context) {
    context.coordinator.text = $text
    if field.stringValue != text { field.stringValue = text }
    field.placeholderString = prompt
  }

  final class Coordinator: NSObject, NSSearchFieldDelegate {
    var text: Binding<String>
    init(text: Binding<String>) { self.text = text }

    @objc func changed(_ sender: NSSearchField) {
      text.wrappedValue = sender.stringValue
    }

    func controlTextDidChange(_ obj: Notification) {
      guard let field = obj.object as? NSSearchField else { return }
      text.wrappedValue = field.stringValue
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
/// Content-fitted: the sheet hugs short content (no scrollbar for 3–4 rows)
/// and scrolls only when content exceeds the max height.
struct DesignSheetHost<Content: View>: View {
  let title: String
  var width: CGFloat = 480
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
      // Native sheets lead with a title; the prop existed but never rendered,
      // so sheets jumped straight into form groups.
      Text(title)
        .font(.headline)
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 20)
        .padding(.top, 16)
        .accessibilityAddTraits(.isHeader)
      ScrollView {
        content()
          .formStyle(.grouped)
          .frame(maxWidth: .infinity)
          .fixedSize(horizontal: false, vertical: true)
      }
      .scrollIndicators(.automatic)
      .frame(maxWidth: .infinity)
      // Hug content: a forced ideal height left a dead void under short
      // forms (the Change sheet). Tall content still caps and scrolls.
      .frame(minHeight: 80, maxHeight: 560)

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
      // Same 20 pt content margin the grouped Form uses, so Cancel/default
      // sit on the group trailing edge instead of the window edge.
      .padding(.trailing, 20)
      .padding(.bottom, 16)
      .frame(maxWidth: .infinity, alignment: .trailing)
    }
    .background(Color(nsColor: .windowBackgroundColor))
    .frame(minWidth: 320, idealWidth: width, maxWidth: 600)
    .fixedSize(horizontal: false, vertical: true)
  }
}
