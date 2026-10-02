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
    HStack(spacing: 5) {
      statusDot
      Text(status.rawValue)
        .foregroundStyle(.secondary)
    }
    .accessibilityLabel("Status: \(status.rawValue)")
  }

  @ViewBuilder
  private var statusDot: some View {
    switch status {
    case .running, .ready:
      Circle()
        .fill(Color.green)
        .frame(width: 7, height: 7)
    case .attention:
      Circle()
        .fill(Color.orange)
        .frame(width: 7, height: 7)
    case .unavailable:
      Circle()
        .fill(Color.red)
        .frame(width: 7, height: 7)
    case .stopped:
      Circle()
        .strokeBorder(Color.secondary.opacity(0.6), lineWidth: 1.5)
        .frame(width: 7, height: 7)
    }
  }
}

/// Sleek horizontal quota capsule matching Image #5:
/// `[Window Label]  [Progress Bar]  [36%]  [Oct 16]`
/// Provider-reported usage for one window: the system progress indicator, the
/// percentage, and the reset instant when the vendor reports one.
///
/// This was a hand-built capsule with fixed pixel widths, a hand-picked track
/// opacity and its own threshold colours. `ProgressView` is the control System
/// Settings uses, it follows the accent, Increase Contrast and Reduce Motion,
/// and it needs no recipe of its own. The threshold tint stays because a
/// nearly-exhausted quota is attention, and `.red` / `.orange` are system
/// colours rather than palette values.
struct DesignQuotaView: View {
  let window: DesignUsageWindow
  var isCompact: Bool = false
  var showsLabel: Bool = true

  private var remainingPercent: Int { max(0, min(100, window.remainingPercent)) }

  var body: some View {
    HStack(spacing: 8) {
      if showsLabel {
        Text(quotaWindowLabel(window))
          .foregroundStyle(.secondary)
          .lineLimit(1)
      }

      ProgressView(value: Double(remainingPercent), total: 100)
        .frame(width: isCompact ? 64 : 110)
        .tint(attentionTint)
        .accessibilityHidden(true)

      Text("\(remainingPercent)%")
        .monospacedDigit()
        .foregroundStyle(.secondary)
        .frame(width: 42, alignment: .trailing)

      Text(
        quotaResetFormatted(
          resetsAtMs: window.resetsAtMs,
          limitWindowSeconds: window.limitWindowSeconds
        ) ?? ""
      )
      .foregroundStyle(.secondary)
      .lineLimit(1)
      .frame(width: 72, alignment: .trailing)
    }
    .accessibilityElement(children: .combine)
    .accessibilityLabel("\(quotaWindowLabel(window)): \(remainingPercent)% remaining")
  }

  private var attentionTint: Color? {
    if remainingPercent <= 5 { return .red }
    if remainingPercent <= 20 { return .orange }
    return nil
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
struct DesignIdentityRow<Icon: View, Trailing: View, Subcontent: View>: View {
  let title: String
  let detail: String
  var issue: String? = nil
  var metadata: String? = nil
  @ViewBuilder var icon: () -> Icon
  @ViewBuilder var subcontent: () -> Subcontent
  @ViewBuilder var trailing: () -> Trailing

  init(
    title: String,
    detail: String,
    issue: String? = nil,
    metadata: String? = nil,
    @ViewBuilder icon: @escaping () -> Icon,
    @ViewBuilder subcontent: @escaping () -> Subcontent,
    @ViewBuilder trailing: @escaping () -> Trailing
  ) {
    self.title = title
    self.detail = detail
    self.issue = issue
    self.metadata = metadata
    self.icon = icon
    self.subcontent = subcontent
    self.trailing = trailing
  }

  init(
    title: String,
    detail: String,
    issue: String? = nil,
    metadata: String? = nil,
    @ViewBuilder icon: @escaping () -> Icon,
    @ViewBuilder trailing: @escaping () -> Trailing
  ) where Subcontent == EmptyView {
    self.init(
      title: title,
      detail: detail,
      issue: issue,
      metadata: metadata,
      icon: icon,
      subcontent: { EmptyView() },
      trailing: trailing
    )
  }

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
          if let metadata, !metadata.isEmpty {
            Text(metadata)
              .font(.caption)
              .foregroundStyle(.secondary)
              .lineLimit(1)
          }
          subcontent()
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

extension DesignIdentityRow where Trailing == EmptyView, Subcontent == EmptyView {
  init(
    title: String,
    detail: String,
    issue: String? = nil,
    metadata: String? = nil,
    @ViewBuilder icon: @escaping () -> Icon
  ) {
    self.init(
      title: title,
      detail: detail,
      issue: issue,
      metadata: metadata,
      icon: icon,
      subcontent: { EmptyView() },
      trailing: { EmptyView() }
    )
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

/// A row-level copy command.
///
/// The glyph never changes — only its colour — so nothing in the row moves when
/// the copy lands. Swapping `doc.on.doc` for a checkmark, or the title "Copy"
/// for "Copied", changes the control's width and shoves every control to its
/// left, which is the jump this replaces.
struct DesignCopyButton: View {
  let copied: Bool
  let label: String
  let identifier: String
  let action: () -> Void

  var body: some View {
    Button(action: action) {
      Image(systemName: "doc.on.doc")
        .foregroundStyle(copied ? .green : .secondary)
    }
    .buttonStyle(.borderless)
    .help(copied ? "Copied" : label)
    .accessibilityLabel(copied ? "Copied" : label)
    .accessibilityIdentifier(identifier)
  }
}

/// One cell of a fixed trailing column grid: the column keeps its width even
/// when the cell has no content.
///
/// `Group { if … { … } }.frame(width:)` does *not* reserve the width — SwiftUI
/// flattens a `Group` whose content is `EmptyView`, and a frame applied to
/// `EmptyView` is dropped — so rows with an empty cell moved left and the
/// trailing controls stopped lining up. A real container fixes the size, which
/// is what makes "the column exists whether or not this row fills it" true.
struct ReservedColumn<Content: View>: View {
  let width: CGFloat
  @ViewBuilder var content: () -> Content

  var body: some View {
    ZStack(alignment: .trailing) {
      content()
    }
    .frame(width: width, alignment: .trailing)
  }
}

/// One grey line inside a group that has no rows, the way System Settings
/// states an empty list.
///
/// It is deliberately not `ContentUnavailableView`: that is the *pane's* empty
/// state (icon, title, description, one action) and belongs to the pane body,
/// not to a group inside a populated one. Before this existed, four panes said
/// "nothing here" four different ways — a filling hero, an icon+text stack, a
/// plain secondary line, and a labelled row — so the same situation looked
/// different on every screen.
struct DesignEmptyRow: View {
  let text: String

  var body: some View {
    Text(text)
      .foregroundStyle(.secondary)
      .multilineTextAlignment(.center)
      .frame(maxWidth: .infinity, alignment: .center)
      .accessibilityIdentifier("state.empty-row")
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

/// Grouped Form shared by every pane. Pane actions are rendered in a plain
/// action row below the Form, like the action rows in System Settings —
/// never in a second inset container, section footer, or toolbar.
///
/// Action controls must be `DesignSheetButton` (NSButton). SwiftUI `Button`
/// under `.searchable` is copied into the window toolbar on macOS 26.
struct DesignGroupedPane<Content: View>: View {
  @ViewBuilder var content: () -> Content
  private let actions: AnyView?
  private let contentIdentifier: String?
  /// Kept for source compatibility with panes that explicitly opt into a
  /// filling layout. Regular panes remain content-sized so the action row
  /// follows the last group instead of being pushed to the window edge.
  var fillHeight: Bool = false

  init(
    fillHeight: Bool = false,
    contentIdentifier: String? = nil,
    @ViewBuilder content: @escaping () -> Content
  ) {
    self.fillHeight = fillHeight
    self.content = content
    self.actions = nil
    self.contentIdentifier = contentIdentifier
  }

  init<ActionContent: View>(
    fillHeight: Bool = false,
    contentIdentifier: String? = nil,
    @ViewBuilder actions: @escaping () -> ActionContent,
    @ViewBuilder content: @escaping () -> Content
  ) {
    self.fillHeight = fillHeight
    self.content = content
    self.actions = AnyView(actions())
    self.contentIdentifier = contentIdentifier
  }

  var body: some View {
    if fillHeight {
      VStack(spacing: 0) {
        groupedForm
          .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
        actionRow
      }
      .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
    } else {
      ScrollView {
        VStack(spacing: 0) {
          groupedForm
            .fixedSize(horizontal: false, vertical: true)
            .frame(maxWidth: .infinity)
          actionRow
        }
        .frame(maxWidth: .infinity, alignment: .top)
      }
      .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
    }
  }

  private var groupedForm: some View {
    Form {
      content()
    }
    .formStyle(.grouped)
    .modifier(DesignOptionalAccessibilityIdentifier(identifier: contentIdentifier))
  }

  @ViewBuilder
  private var actionRow: some View {
    if let actions {
      actions
        .frame(maxWidth: .infinity, alignment: .trailing)
        .padding(.horizontal)
        .padding(.vertical, 8)
    }
  }
}

private struct DesignOptionalAccessibilityIdentifier: ViewModifier {
  let identifier: String?

  @ViewBuilder
  func body(content: Content) -> some View {
    if let identifier {
      content.accessibilityIdentifier(identifier)
    } else {
      content
    }
  }
}

/// Plain trailing action row directly below the grouped Form, matching the
/// action placement used by System Settings for pane-level commands without
/// adding another inset background around the buttons.
struct DesignPaneActions<Content: View>: View {
  @ViewBuilder var content: () -> Content

  var body: some View {
    // Keep the native trailing action row at normal widths, but do not let
    // pane commands clip when the detail column becomes narrow. ViewThatFits
    // selects the system-styled stacked fallback without introducing a
    // custom flow layout or a second container around the controls.
    ViewThatFits(in: .horizontal) {
      HStack(spacing: 12) {
        Spacer(minLength: 0)
        content()
      }
      VStack(alignment: .trailing, spacing: 8) {
        content()
      }
    }
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
///
/// A sheet's primary action sets `keyEquivalent` to Return, which makes AppKit
/// treat it as the window's default button and draw the accent fill, the
/// pressed state, the disabled bezel, and Increase Contrast itself. Overriding
/// `bezelColor` / `contentTintColor` to fake that fill is what stopped the
/// primary button from matching the system button beside it.
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
    if let identifier {
      button.setAccessibilityIdentifier(identifier)
    }
    button.setContentHuggingPriority(.required, for: .horizontal)
    button.setContentCompressionResistancePriority(.required, for: .horizontal)
    return button
  }

  func updateNSView(_ button: NSButton, context: Context) {
    button.title = title
    button.keyEquivalent = keyEquivalent
    button.isEnabled = enabled
    button.hasDestructiveAction = destructive
    if let identifier {
      button.setAccessibilityIdentifier(identifier)
    }
    context.coordinator.action = action
  }

  final class Coordinator: NSObject {
    var action: () -> Void
    init(action: @escaping () -> Void) { self.action = action }
    @objc func click() { action() }
  }
}

/// Native macOS sheet host. Like a System Settings sheet, there is no
/// titlebar: the title sits inside the sheet above the content, and the
/// actions live in a separated bottom bar rather than the window title bar.
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
    VStack(spacing: 0) {
      VStack(alignment: .leading, spacing: 18) {
        Text(title)
          .font(.title2.weight(.semibold))
          .frame(maxWidth: .infinity, alignment: .leading)
          .padding(.horizontal)
          .accessibilityAddTraits(.isHeader)

        ScrollView {
          content()
            .formStyle(.grouped)
            .scrollContentBackground(.hidden)
            .frame(maxWidth: .infinity)
            .frame(maxWidth: .infinity, alignment: .topLeading)
            .fixedSize(horizontal: false, vertical: true)
        }
        // Keep short states content-sized while retaining a cap for long
        // provider feedback. The outer scroll view owns overflow; content
        // must not force the sheet past this cap or overlap the action bar.
        .background(Color.clear)
        .scrollIndicators(.automatic)
        .frame(maxWidth: .infinity, minHeight: 80, maxHeight: 560, alignment: .top)
      }
      .padding(.vertical)

      // No drawn hairline above the buttons: a native sheet separates the
      // action bar by spacing, not by a rule this view paints itself.
      HStack(spacing: 8) {
        Spacer(minLength: 0)
        if showsCancel {
          DesignSheetButton(
            title: "Cancel",
            keyEquivalent: "\u{1b}",
            identifier: "sheet.cancel",
            action: onCancel
          )
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
      .padding()
    }
    .background(Color(nsColor: .windowBackgroundColor))
    .frame(minWidth: 320, idealWidth: width, maxWidth: 600)
    .fixedSize(horizontal: false, vertical: true)
  }
}
