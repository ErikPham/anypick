import AppKit
import SwiftUI

struct ActivityView: View {
  @ObservedObject var model: MacOSDesignModel
  @State private var confirmClear = false
  @State private var searchQuery = ""

  var body: some View {
    activityPane
    .searchable(
      text: $searchQuery,
      placement: .toolbar,
      prompt: model.activityShowsLogs ? "Search logs" : "Search events"
    )
    .toolbar {
      if model.activityShowsLogs {
        ToolbarItem(placement: .primaryAction) {
          logSourceMenu
        }
      }
    }
    .onChange(of: model.activityShowsLogs) { _, enabled in
      searchQuery = ""
      model.setActivityShowsLogs(enabled)
      model.setLogPolling(enabled)
    }
    .onChange(of: model.selectedLogSourceID) {
      if model.activityShowsLogs { model.setLogPolling(true) }
    }
    .onAppear {
      if model.activityShowsLogs { model.setLogPolling(true) }
    }
    .onDisappear {
      model.setLogPolling(false)
    }
    .confirmationDialog(
      "Clear Visible Logs?",
      isPresented: $confirmClear,
      titleVisibility: .visible
    ) {
      Button("Clear", role: .destructive) { model.clearActivity() }
      Button("Cancel", role: .cancel) {}
    } message: {
      Text("Accounts, apps, and proxies are unchanged.")
    }
  }

  @ViewBuilder
  private var activityPane: some View {
    if model.activityShowsLogs {
      DesignGroupedPane {
        Section {
          paneHeader
          modeControl
          logStateNotice
          logsRows
        } footer: {
          Text("Sensitive values are redacted before logs reach this window.")
        }
      } actions: {
        Button("Clear Log…", role: .destructive) { confirmClear = true }
          .disabled(filteredLogLines.isEmpty)
          .accessibilityIdentifier("activity.clear-log")
      }
    } else {
      DesignGroupedPane {
        Section {
          paneHeader
          modeControl
          if filteredActivity.isEmpty {
            emptyState(
              symbol: "clock.arrow.circlepath",
              message: searchQuery.isEmpty
                ? "Activity will appear here."
                : "No events match your search."
            )
          } else {
            ForEach(eventSections, id: \.key) { section in
              dayHeader(section.key)
              ForEach(section.items) { item in
                eventRow(item)
              }
            }
          }
        }
      }
    }
  }

  /// Subtitle only — the toolbar already titles the pane "Activity".
  private var paneHeader: some View {
    Text("Recent changes and recovery information")
      .font(.subheadline)
      .foregroundStyle(.secondary)
      .frame(maxWidth: .infinity, alignment: .leading)
      .listRowInsets(rowInsets)
      .accessibilityIdentifier("activity.header")
  }

  /// Activity rows use the compact rhythm from the design while SwiftUI owns
  /// the actual grouped Form and separators.
  private let rowInsets = EdgeInsets(top: 6, leading: 12, bottom: 6, trailing: 12)

  /// Native segmented Events/Live Logs picker with system styling.
  private var modeControl: some View {
    HStack(spacing: 12) {
      Picker("View", selection: $model.activityShowsLogs) {
        Text("Events").tag(false)
        Text("Live Logs").tag(true)
      }
      .pickerStyle(.segmented)
      .frame(maxWidth: 240)
      .accessibilityIdentifier("activity.mode.picker")
      Spacer(minLength: 12)
      if model.activityShowsLogs, model.logState == .ready {
        Label {
          Text("Live")
        } icon: {
          Image(systemName: "dot.radiowaves.left.and.right")
        }
        .foregroundStyle(.green)
        .accessibilityIdentifier("activity.logs.live")
      }
    }
    .padding(.vertical, 10)
    .padding(.horizontal, 12)
    .accessibilityElement(children: .contain)
    .accessibilityLabel("Activity view")
    .accessibilityIdentifier("activity.mode")
  }

  /// The Pencil filter button maps to a native toolbar Menu. The selected
  /// source receives the standard checkmark treatment from NSMenu. Design
  /// ngJKM draws it icon-only (three filter lines, no title).
  private var logSourceMenu: some View {
    Menu {
      ForEach(model.logSources) { source in
        Button {
          model.selectLogSource(source.id)
        } label: {
          if source.id == model.selectedLogSourceID {
            Label(source.label, systemImage: "checkmark")
          } else {
            Text(source.label)
          }
        }
      }
    } label: {
      Label("Filter Logs", systemImage: "line.3.horizontal.decrease")
    }
    .labelStyle(.iconOnly)
    .help("Filter logs by provider")
    .disabled(model.logSources.isEmpty)
    .accessibilityIdentifier("activity.logs.filter")
  }

  /// Day label row ("Today") as a semantic section header.
  private func dayHeader(_ label: String) -> some View {
    Text(label)
      .foregroundStyle(.secondary)
      .listRowInsets(rowInsets)
      .accessibilityAddTraits(.isHeader)
      .accessibilityLabel(label)
  }

  private func eventRow(_ item: DesignActivity) -> some View {
    HStack(alignment: .center, spacing: 10) {
      Text(item.time)
        .foregroundStyle(.secondary)
        .monospacedDigit()
        .frame(width: 62, alignment: .leading)
        .accessibilityHidden(true)
      // Success events keep their own icon; problem states use the canonical
      // status glyph so they never read as success.
      Image(
        systemName: item.status == .ready || item.status == .running
          ? item.symbol : item.status.symbol
      )
      .foregroundStyle(item.status.color)
      .frame(width: 16)
      .accessibilityHidden(true)
      VStack(alignment: .leading, spacing: 2) {
        HStack(spacing: 6) {
          if let provider = item.provider {
            DesignProviderIcon(provider: provider, fallbackSymbol: item.symbol, size: 16)
              .accessibilityHidden(true)
          }
          Text(item.title)
            .fontWeight(.medium)
            .lineLimit(1)
        }
        if !item.detail.isEmpty {
          Text(item.detail)
            .font(.caption)
            .foregroundStyle(.secondary)
            .lineLimit(2)
        }
      }
    }
    .listRowInsets(rowInsets)
    .accessibilityElement(children: .combine)
    .accessibilityLabel(activityAccessibilityLabel(for: item))
    .accessibilityIdentifier("activity.event.\(item.id)")
  }

  /// Day buckets in list order; the model already returns newest first.
  private var eventSections: [(key: String, items: [DesignActivity])] {
    var sections: [(String, [DesignActivity])] = []
    for item in filteredActivity {
      if sections.last?.0 == item.day {
        sections[sections.count - 1].1.append(item)
      } else {
        sections.append((item.day, [item]))
      }
    }
    return sections.map { (key: $0.0, items: $0.1) }
  }

  @ViewBuilder
  private var logsRows: some View {
    // The design keeps the console frame always visible — a bounded,
    // independently scrolling region — with placeholder copy inside it when
    // there is nothing to show. Conditionally removing the frame left a void
    // and made the pane jump between states.
    ActivityLogViewer(lines: Array(filteredLogLines.suffix(200)), placeholder: logPlaceholder)
      .frame(maxWidth: .infinity, minHeight: 320, maxHeight: 320)
      .listRowBackground(Color.clear)
      .listRowInsets(rowInsets)
  }

  /// Passive copy inside the console when it has no lines. Status + actions
  /// still live in the notice above; this only fills the frame.
  private var logPlaceholder: String? {
    guard filteredLogLines.isEmpty else { return nil }
    if !searchQuery.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
      return "No log lines match your search."
    }
    switch model.logState {
    case .loading: return "Loading logs…"
    case .stopped: return "Proxy is not running — start it to stream new entries."
    case .empty: return "No log entries yet."
    case .error: return "Couldn’t load logs."
    case .unavailable: return "No log source available."
    case .idle, .ready: return "Output appears here while the selected provider is running."
    }
  }

  private func emptyState(symbol: String, message: String) -> some View {
    VStack(spacing: 6) {
      Image(systemName: symbol)
        .font(.title3)
        .foregroundStyle(.tertiary)
      Text(message)
        .foregroundStyle(.secondary)
        .multilineTextAlignment(.center)
    }
    .frame(maxWidth: .infinity, minHeight: 120)
    .accessibilityElement(children: .combine)
    .listRowInsets(rowInsets)
  }

  @ViewBuilder
  private var logStateNotice: some View {
    switch model.logState {
    case .idle, .ready:
      EmptyView()
    case .loading:
      HStack(spacing: 8) {
        ProgressView()
          .controlSize(.small)
        Text(model.logStateMessage ?? "Loading logs…")
          .foregroundStyle(.secondary)
      }
      .accessibilityIdentifier("activity.logs.loading")
      .listRowInsets(rowInsets)
    case .empty:
      logNotice(
        title: "No Logs Yet",
        message: model.logStateMessage ?? "The service has not written any log entries.",
        symbol: "doc.text"
      )
    case .stopped:
      logNotice(
        title: "Proxy Is Not Running",
        message: model.logStateMessage ?? "Prior logs remain visible.",
        symbol: "stop.circle",
        actionTitle: stoppedActionTitle,
        action: startStoppedSource
      )
    case .error:
      logNotice(
        title: "Couldn’t Load Logs",
        message: model.logStateMessage ?? "Showing the most recent logs that loaded successfully.",
        symbol: "exclamationmark.triangle",
        actionTitle: "Try Again",
        action: model.refreshSelectedLogSource
      )
    case .unavailable:
      logNotice(
        title: "Selected Log Is No Longer Available",
        message: model.logStateMessage ?? "Choose another service or return to Activity.",
        symbol: "questionmark.folder"
      )
    }
  }

  private func logNotice(
    title: String,
    message: String,
    symbol: String,
    actionTitle: String? = nil,
    action: (() -> Void)? = nil
  ) -> some View {
    LabeledContent {
      if let actionTitle, let action {
        Button(actionTitle, action: action)
      }
    } label: {
      Label {
        VStack(alignment: .leading, spacing: 2) {
          Text(title)
          Text(message)
            .foregroundStyle(.secondary)
        }
      } icon: {
        Image(systemName: symbol)
      }
    }
    .accessibilityIdentifier("activity.logs.\(model.logState.rawValue)")
    .listRowInsets(rowInsets)
  }

  /// The stopped notice offers the Start right here: sending the user to
  /// the Proxies pane for the one action this screen is about is a dead end.
  private var stoppedProxyID: String? {
    let id = model.selectedLogSourceID
    guard !id.isEmpty, model.proxies.contains(where: { $0.id == id }) else { return nil }
    return id
  }

  private var stoppedActionTitle: String? {
    stoppedProxyID == nil ? nil : "Start Proxy"
  }

  private func startStoppedSource() {
    guard let id = stoppedProxyID else { return }
    model.setProxyEnabled(id: id, enabled: true)
  }

  private var filteredActivity: [DesignActivity] {
    let query = searchQuery.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !query.isEmpty else { return model.activity }
    return model.activity.filter {
      [$0.time, $0.title, $0.detail].contains { $0.localizedCaseInsensitiveContains(query) }
    }
  }

  private var filteredLogLines: [String] {
    let query = searchQuery.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !query.isEmpty else { return model.liveLogLines }
    return model.liveLogLines.filter { $0.localizedCaseInsensitiveContains(query) }
  }
}

extension Color {
  /// Log console uses system colors so Increase Contrast follows the OS.
  static var logConsoleText: Color { .white }
  static var logConsoleAccent: Color { .green }
}

/// The log console from design EKVHg ("Raw Log Viewer"): pure-black frame
/// both modes, 13pt mono $log-text lines, redaction note in $log-accent
/// green, 12/14 padding. Selectable, independently scrolling AppKit text
/// view — no bezel, fixed 320 height like the design frame.
private struct ActivityLogViewer: NSViewRepresentable {
  let lines: [String]
  /// Shown in secondary color when lines is empty so the console frame is
  /// never a dead black box. Nil disables the placeholder.
  let placeholder: String?

  private static let logFont: NSFont =
    NSFont(name: "JetBrainsMono-Regular", size: 13)
    ?? NSFont(name: "JetBrains Mono", size: 13)
    ?? .monospacedSystemFont(ofSize: 13, weight: .regular)

  private static let lineSpacing: CGFloat = 7

  func makeNSView(context: Context) -> NSScrollView {
    let scrollView = NSScrollView()
    scrollView.hasVerticalScroller = true
    scrollView.autohidesScrollers = true
    scrollView.borderType = .noBorder
    scrollView.drawsBackground = false
    scrollView.backgroundColor = .black
    scrollView.setAccessibilityIdentifier("activity.logs")

    let textView = NSTextView()
    textView.isEditable = false
    textView.isRichText = true
    textView.isSelectable = true
    textView.drawsBackground = true
    textView.backgroundColor = .black
    textView.textColor = .white
    textView.font = Self.logFont
    textView.textContainerInset = NSSize(width: 14, height: 12)
    textView.textContainer?.widthTracksTextView = true
    scrollView.documentView = textView
    update(textView, in: scrollView)
    return scrollView
  }

  func updateNSView(_ scrollView: NSScrollView, context: Context) {
    guard let textView = scrollView.documentView as? NSTextView else { return }
    update(textView, in: scrollView)
  }

  private static func paragraph() -> NSParagraphStyle {
    let style = NSMutableParagraphStyle()
    style.lineSpacing = lineSpacing
    return style
  }

  private func update(_ textView: NSTextView, in scrollView: NSScrollView) {
    // Placeholder mode: grey, tracked by content so the color flips back
    // the moment real lines arrive.
    if lines.isEmpty, let placeholder {
      let styled = NSAttributedString(
        string: placeholder,
        attributes: [
          .font: Self.logFont,
          .foregroundColor: NSColor.secondaryLabelColor,
          .paragraphStyle: Self.paragraph(),
        ])
      if !textView.attributedString().isEqual(to: styled) {
        textView.textStorage?.setAttributedString(styled)
      }
      return
    }
    let full = NSMutableAttributedString()
    let bodyAttributes: [NSAttributedString.Key: Any] = [
      .font: Self.logFont,
      .foregroundColor: NSColor.white,
      .paragraphStyle: Self.paragraph(),
    ]
    let accentAttributes: [NSAttributedString.Key: Any] = [
      .font: Self.logFont,
      .foregroundColor: NSColor.systemGreen,
      .paragraphStyle: Self.paragraph(),
    ]
    for (index, line) in lines.enumerated() {
      // The design paints the redaction note green ($log-accent); every
      // other line is $log-text.
      let attrs =
        line.localizedCaseInsensitiveContains("redact")
        ? accentAttributes : bodyAttributes
      full.append(NSAttributedString(string: line, attributes: attrs))
      if index < lines.count - 1 {
        full.append(NSAttributedString(string: "\n", attributes: bodyAttributes))
      }
    }
    if !textView.attributedString().isEqual(to: full) {
      // Pin the offset before the layout changes. When the user was already
      // at the bottom (the common tail-following case), re-pin to the new
      // bottom after the update instead of leaving them one page up.
      let visible = scrollView.documentVisibleRect
      let contentHeight =
        (scrollView.documentView?.bounds.height ?? visible.maxY) - visible.height
      let wasAtBottom = visible.minY >= contentHeight - 4
      let savedOrigin = scrollView.contentView.bounds.origin
      textView.textStorage?.setAttributedString(full)
      textView.layoutManager?.ensureLayout(for: textView.textContainer!)
      if wasAtBottom {
        textView.scrollToEndOfDocument(nil)
      } else {
        scrollView.contentView.scroll(to: savedOrigin)
        scrollView.reflectScrolledClipView(scrollView.contentView)
      }
    }
  }
}
