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
      prompt: model.activityShowsLogs ? "Search Logs" : "Search Events"
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

  /// Activity rows use the compact rhythm from the design while SwiftUI owns
  /// the actual grouped Form and separators.
  private let rowInsets = EdgeInsets(top: 6, leading: 12, bottom: 6, trailing: 12)

  /// This is an AppKit tab group, matching the control exposed by System
  /// Settings. `labelsHidden` lets this unlabeled row occupy the full Form
  /// width instead of beginning at SwiftUI's labeled-control column.
  private var modeControl: some View {
    HStack(spacing: 12) {
      ActivityModeControl(showsLogs: $model.activityShowsLogs)
        .frame(width: 240, height: 28)
      Spacer(minLength: 12)
      if model.activityShowsLogs, model.logState == .ready {
        Label("Live", systemImage: "dot.radiowaves.left.and.right")
          .font(.caption)
          .foregroundStyle(.green)
          .accessibilityIdentifier("activity.logs.live")
      }
    }
      .frame(maxWidth: .infinity)
      .listRowInsets(EdgeInsets(top: 10, leading: 12, bottom: 10, trailing: 12))
      .labelsHidden()
      .accessibilityLabel("Activity")
      .accessibilityIdentifier("activity.mode")
  }

  /// The Pencil filter button maps to a native toolbar Menu. The selected
  /// source receives the standard checkmark treatment from NSMenu.
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
    .help("Filter logs by provider")
    .disabled(model.logSources.isEmpty)
    .accessibilityIdentifier("activity.logs.filter")
  }

  /// Day label row ("Today"). Secondary text inside the same group —
  /// the grouped Form owns row metrics, we only supply content.
  private func dayHeader(_ label: String) -> some View {
    Text(label)
      .foregroundStyle(.secondary)
      .listRowInsets(rowInsets)
  }

  private func eventRow(_ item: DesignActivity) -> some View {
    HStack(alignment: .center, spacing: 10) {
      Text(item.time)
        .foregroundStyle(.secondary)
        .monospacedDigit()
        .frame(width: 62, alignment: .leading)
      Image(
        systemName: item.status == .attention
          ? "exclamationmark.circle.fill" : item.symbol
      )
      .foregroundStyle(item.status == .attention ? .red : .green)
      .frame(width: 16)
      .accessibilityLabel(item.status.rawValue)
      VStack(alignment: .leading, spacing: 2) {
        HStack(spacing: 6) {
          if let provider = item.provider {
            DesignProviderIcon(provider: provider, fallbackSymbol: item.symbol, size: 16)
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
    if filteredLogLines.isEmpty, (model.logState == .ready || model.logState == .idle) {
      emptyState(
        symbol: "doc.plaintext",
        message:
          searchQuery.isEmpty
          ? "Output appears here while the selected provider is running."
          : "No log lines match your search."
      )
    } else {
      ActivityLogViewer(lines: Array(filteredLogLines.suffix(200)))
        .frame(maxWidth: .infinity, minHeight: 320)
        .listRowBackground(Color.clear)
        .listRowInsets(rowInsets)
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
        symbol: "stop.circle"
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

/// AppKit exposes this as a tab group to accessibility, which is also how the
/// equivalent control in System Settings is represented. SwiftUI's segmented
/// Picker gets different Form-column metrics on macOS.
private struct ActivityModeControl: NSViewRepresentable {
  @Binding var showsLogs: Bool

  func makeCoordinator() -> Coordinator {
    Coordinator(showsLogs: $showsLogs)
  }

  func makeNSView(context: Context) -> NSSegmentedControl {
    let control = NSSegmentedControl(
      labels: ["Events", "Live Logs"],
      trackingMode: .selectOne,
      target: context.coordinator,
      action: #selector(Coordinator.selectionChanged(_:))
    )
    control.segmentStyle = .automatic
    control.setAccessibilityRole(.tabGroup)
    control.setAccessibilityLabel("Activity")
    control.setWidth(120, forSegment: 0)
    control.setWidth(120, forSegment: 1)
    control.selectedSegment = showsLogs ? 1 : 0
    return control
  }

  func updateNSView(_ control: NSSegmentedControl, context: Context) {
    context.coordinator.showsLogs = $showsLogs
    let selection = showsLogs ? 1 : 0
    if control.selectedSegment != selection {
      control.selectedSegment = selection
    }
  }

  final class Coordinator: NSObject {
    var showsLogs: Binding<Bool>

    init(showsLogs: Binding<Bool>) {
      self.showsLogs = showsLogs
    }

    @objc func selectionChanged(_ sender: NSSegmentedControl) {
      showsLogs.wrappedValue = sender.selectedSegment == 1
    }
  }
}

/// The log is a selectable, independently scrolling AppKit text view. It
/// keeps native colors and scrollbars instead of painting a web-console card.
private struct ActivityLogViewer: NSViewRepresentable {
  let lines: [String]

  func makeNSView(context: Context) -> NSScrollView {
    let scrollView = NSScrollView()
    scrollView.hasVerticalScroller = true
    scrollView.autohidesScrollers = true
    scrollView.borderType = .bezelBorder
    scrollView.setAccessibilityIdentifier("activity.logs")

    let textView = NSTextView()
    textView.isEditable = false
    textView.isRichText = false
    textView.isSelectable = true
    textView.drawsBackground = true
    textView.backgroundColor = .textBackgroundColor
    textView.textColor = .labelColor
    textView.font = .monospacedSystemFont(ofSize: NSFont.smallSystemFontSize, weight: .regular)
    textView.textContainerInset = NSSize(width: 8, height: 8)
    textView.textContainer?.widthTracksTextView = true
    scrollView.documentView = textView
    update(textView)
    return scrollView
  }

  func updateNSView(_ scrollView: NSScrollView, context: Context) {
    guard let textView = scrollView.documentView as? NSTextView else { return }
    update(textView)
  }

  private func update(_ textView: NSTextView) {
    let string = lines.joined(separator: "\n")
    guard textView.string != string else { return }
    textView.string = string
  }
}
