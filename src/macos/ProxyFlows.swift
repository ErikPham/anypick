import SwiftUI

struct PublishedModelsView: View {
  @ObservedObject var model: MacOSDesignModel
  @State private var search = ""

  private func matchesSearch(_ item: DesignPublishedModel) -> Bool {
    search.isEmpty
      || item.modelID.localizedCaseInsensitiveContains(search)
      || item.ownerName.localizedCaseInsensitiveContains(search)
      || item.ownerProvider.localizedCaseInsensitiveContains(search)
  }

  private var published: [DesignPublishedModel] {
    model.publishedModels.filter { !$0.needsChoice && matchesSearch($0) }
  }

  private var needsChoice: [DesignPublishedModel] {
    model.publishedModels.filter { $0.needsChoice && matchesSearch($0) }
  }

  private var ownedGroups:
    [(id: String, name: String, provider: String, models: [DesignPublishedModel])]
  {
    let grouped = Dictionary(grouping: published) { "\($0.ownerProvider)\u{1e}\($0.ownerName)" }
    return grouped.keys.sorted().compactMap { key in
      guard let models = grouped[key], let first = models.first else { return nil }
      return (
        key,
        first.ownerName,
        first.ownerProvider,
        models.sorted { $0.modelID.localizedStandardCompare($1.modelID) == .orderedAscending }
      )
    }
  }

  var body: some View {
    Group {
      if published.isEmpty && needsChoice.isEmpty {
        ContentUnavailableView {
          Label(
            search.isEmpty ? "No Published Models" : "No Results",
            systemImage: search.isEmpty ? "square.stack.3d.up.slash" : "magnifyingglass"
          )
        } description: {
          Text(
            search.isEmpty
              ? "Turn on an account and resolve overlapping models, then they appear here."
              : "No models match this search."
          )
        }
      } else {
        Form {
          if !needsChoice.isEmpty {
            Section {
              ForEach(needsChoice) { item in
                LabeledContent {
                  Text("Needs a choice")
                    .foregroundStyle(.secondary)
                } label: {
                  Text(item.modelID)
                    .font(.body.monospaced())
                    .lineLimit(1)
                    .truncationMode(.middle)
                }
                .accessibilityIdentifier("published-models.row.\(item.modelID)")
              }
            } header: {
              Text("Needs a Choice")
            } footer: {
              Text("Pick an account on Proxy Hub, then these IDs publish.")
            }
          }

          ForEach(ownedGroups, id: \.id) { group in
            Section {
              ForEach(group.models) { item in
                LabeledContent {
                  Text(group.provider)
                    .foregroundStyle(.secondary)
                } label: {
                  Text(item.modelID)
                    .font(.body.monospaced())
                    .lineLimit(1)
                    .truncationMode(.middle)
                }
                .accessibilityIdentifier("published-models.row.\(item.modelID)")
              }
            } header: {
              Text(group.name)
            }
          }
        }
        .formStyle(.grouped)
      }
    }
    .navigationTitle("Published Models")
    .searchable(text: $search, placement: .toolbar, prompt: "Search")
  }
}
