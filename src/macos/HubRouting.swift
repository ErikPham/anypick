import Foundation

/// Compiles fixture Hub routes the same way core does: one catalog identity
/// is a credential choice; the same model id in two catalogs is an overlap.
enum DesignHubRouting {
  static func isEligible(_ account: DesignAccount) -> Bool {
    account.kind == .saved || account.id == "openrouter"
  }

  static func warning(for account: DesignAccount) -> String? {
    account.id == "personal-builder" ? "Catalog unavailable" : nil
  }

  static func defaultEnabledIDs(in accounts: [DesignAccount]) -> Set<String> {
    Set([
      "claude-work",
      "claude-personal",
      "claude-team",
      "codex-work",
      "google-pro",
      "grok-work",
      "openrouter",
      "personal-builder",
    ]).intersection(Set(accounts.map(\.id)))
  }

  static func sources(from accounts: [DesignAccount], enabledIDs: Set<String>) -> [DesignHubSource] {
    accounts.filter(isEligible).map { account in
      DesignHubSource(
        id: account.id,
        name: account.name,
        detail: account.detail,
        provider: account.provider,
        symbol: account.symbol,
        enabled: enabledIDs.contains(account.id),
        warning: warning(for: account)
      )
    }
  }

  static func catalogs(from accounts: [DesignAccount]) -> [DesignHubCatalog] {
    accounts.filter(isEligible).compactMap { account in
      guard let models = models(for: account.id), !models.isEmpty else { return nil }
      return DesignHubCatalog(
        accountID: account.id,
        catalogID: catalogID(for: account),
        models: models
      )
    }
  }

  static func compile(
    sources: [DesignHubSource],
    catalogs: [DesignHubCatalog],
    owners: [String: String]
  ) -> (
    sources: [DesignHubSource],
    models: [DesignPublishedModel],
    conflicts: [DesignRoutingConflict]
  ) {
    let sourceByID = Dictionary(uniqueKeysWithValues: sources.map { ($0.id, $0) })
    let publishingIDs = Set(
      sources.filter { $0.enabled && $0.warning == nil }.map(\.id)
    )
    let activeCatalogs = catalogs.filter { publishingIDs.contains($0.accountID) }

    var byModel: [String: [String: [String]]] = [:]
    for catalog in activeCatalogs {
      for model in catalog.models {
        var catalogsForModel = byModel[model] ?? [:]
        var accounts = catalogsForModel[catalog.catalogID] ?? []
        if !accounts.contains(catalog.accountID) {
          accounts.append(catalog.accountID)
        }
        catalogsForModel[catalog.catalogID] = accounts
        byModel[model] = catalogsForModel
      }
    }

    var published: [DesignPublishedModel] = []
    var choiceGroups: [String: (catalogID: String, models: [String], accountIDs: [String])] = [:]
    var overlapGroups: [String: (models: [String], accountIDs: [String])] = [:]

    for model in byModel.keys.sorted() {
      guard let catalogGroups = byModel[model] else { continue }
      let accountIDs = catalogGroups.values.flatMap { $0 }.sorted()
      if let owner = owners[model], accountIDs.contains(owner),
         let source = sourceByID[owner] {
        published.append(publishedModel(model, source: source))
        continue
      }
      if catalogGroups.count == 1, let (catalogID, candidates) = catalogGroups.first {
        if candidates.count == 1, let accountID = candidates.first, let source = sourceByID[accountID]
        {
          published.append(publishedModel(model, source: source))
          continue
        }
        let key = "choice|\(catalogID)|\(candidates.sorted().joined(separator: ","))"
        var group = choiceGroups[key] ?? (catalogID, [], candidates.sorted())
        group.models.append(model)
        choiceGroups[key] = group
        continue
      }
      let key = "overlap|\(accountIDs.joined(separator: ","))"
      var group = overlapGroups[key] ?? ([], accountIDs)
      group.models.append(model)
      overlapGroups[key] = group
    }

    var conflicts: [DesignRoutingConflict] = []
    for (index, group) in choiceGroups.values.sorted(by: { $0.catalogID < $1.catalogID })
      .enumerated()
    {
      let models = group.models.sorted()
      let candidates = candidates(group.accountIDs, sources: sourceByID, actionPrefix: "choice-\(index + 1)")
      conflicts.append(
        DesignRoutingConflict(
          id: "hub-source-choice-\(index + 1)",
          kind: .sourceChoice,
          title: displayName(forCatalog: group.catalogID),
          detail: modelSummary(models),
          models: models,
          needsAttention: true,
          candidates: candidates
        )
      )
    }
    let overlapList = overlapGroups.values.sorted {
      $0.accountIDs.joined() < $1.accountIDs.joined()
    }
    for (index, group) in overlapList.enumerated() {
      let models = group.models.sorted()
      let candidates = candidates(
        group.accountIDs,
        sources: sourceByID,
        actionPrefix: "overlap-\(index + 1)"
      )
      conflicts.append(
        DesignRoutingConflict(
          id: "hub-conflict-\(index + 1)",
          kind: .modelOverlap,
          title: models.count == 1 ? models[0] : "\(models.count) Models",
          detail: modelSummary(models),
          models: models,
          needsAttention: true,
          candidates: candidates
        )
      )
    }

    let resolved = sources.map { source -> DesignHubSource in
      var next = source
      if let catalog = catalogs.first(where: { $0.accountID == source.id }),
         source.enabled, source.warning == nil {
        next.modelCount = catalog.models.count
      } else {
        next.modelCount = 0
      }
      return next
    }

    for conflict in conflicts {
      for model in conflict.models {
        published.append(
          DesignPublishedModel(
            modelID: model,
            ownerAccountID: nil,
            ownerName: "Needs a choice",
            ownerProvider: conflict.candidates.first?.provider ?? "",
            needsChoice: true
          )
        )
      }
    }

    return (
      resolved,
      published.sorted { $0.modelID.localizedStandardCompare($1.modelID) == .orderedAscending },
      conflicts
    )
  }

  static func modelSummary(_ models: [String]) -> String {
    guard let first = models.first else { return "No models" }
    if models.count == 1 { return first }
    if models.count == 2 { return "\(first) and \(models[1])" }
    return "\(models.count) models, including \(first)"
  }

  private static func publishedModel(_ model: String, source: DesignHubSource) -> DesignPublishedModel {
    DesignPublishedModel(
      modelID: model,
      ownerAccountID: source.id,
      ownerName: source.name,
      ownerProvider: source.provider,
      needsChoice: false
    )
  }

  private static func candidates(
    _ accountIDs: [String],
    sources: [String: DesignHubSource],
    actionPrefix: String
  ) -> [DesignRoutingCandidate] {
    accountIDs.compactMap { accountID in
      guard let source = sources[accountID] else { return nil }
      return DesignRoutingCandidate(
        id: source.id,
        label: source.name,
        detail: source.detail,
        provider: source.provider,
        actionID: "\(actionPrefix):\(source.id)"
      )
    }
  }

  private static func catalogID(for account: DesignAccount) -> String {
    switch account.provider.lowercased() {
    case "claude", "anthropic": return "claude"
    case "codex", "openai": return "codex"
    case "gemini", "google": return "gemini"
    case "grok", "xai": return "grok"
    case "kiro": return "kiro"
    case "openrouter": return "openrouter"
    default: return account.provider.lowercased()
    }
  }

  private static func displayName(forCatalog catalogID: String) -> String {
    switch catalogID {
    case "claude": return "Claude"
    case "codex": return "Codex"
    case "gemini": return "Gemini"
    case "grok": return "Grok"
    case "kiro": return "Kiro"
    case "openrouter": return "OpenRouter"
    default: return catalogID.capitalized
    }
  }

  private static func models(for accountID: String) -> [String]? {
    switch accountID {
    case "claude-work":
      return claudeShared + ["claude-opus-4-1-thinking"]
    case "claude-personal":
      return claudeShared
    case "claude-team":
      return claudeShared + ["claude-3-5-sonnet-20241022"]
    case "codex-work":
      return codexShared + ["gpt-5.1-codex-spark"]
    case "codex-personal":
      return codexShared
    case "google-pro":
      return [
        "gemini-2.5-pro",
        "gemini-2.5-flash",
        "gemini-2.5-flash-lite",
        "gemini-2.0-flash",
        "gemini-1.5-pro",
      ]
    case "grok-work":
      return ["grok-3", "grok-3-mini", "grok-4", "grok-2", "grok-2-mini"]
    case "personal-builder":
      return ["kiro-auto", "kiro-sonnet"]
    case "openrouter":
      return [
        "openrouter/auto",
        "anthropic/claude-3.7-sonnet",
        "google/gemini-2.5-pro",
        "meta-llama/llama-3.3-70b-instruct",
        "qwen/qwen3-coder",
        "mistralai/mistral-large",
        "deepseek/deepseek-chat",
        "claude-sonnet-4",
        "claude-haiku-4-5",
        "gpt-5.1-codex",
        "grok-3",
      ]
    default:
      return nil
    }
  }

  private static let claudeShared = [
    "claude-opus-4-1",
    "claude-opus-4",
    "claude-sonnet-4",
    "claude-sonnet-4-5",
    "claude-haiku-4-5",
    "claude-3-7-sonnet-latest",
    "claude-3-5-sonnet-latest",
    "claude-3-5-haiku-latest",
    "claude-3-opus-latest",
    "claude-3-haiku-20240307",
  ]

  private static let codexShared = [
    "gpt-5.1-codex",
    "gpt-5.1-codex-mini",
    "gpt-5.1-codex-max",
    "GPT-5.6 Codex",
    "o3",
    "o4-mini",
    "o3-mini",
    "gpt-4.1",
    "gpt-4.1-mini",
  ]
}