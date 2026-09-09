import SwiftUI

enum AccountsSheet: String, Identifiable {
  case addAccount
  case addGateway

  var id: String { rawValue }
}

struct AddSavedAccountSheet: View {
  private enum SourceMode: String, CaseIterable, Identifiable {
    case thisMac = "On This Mac"
    case newAccount = "New Account"
    var id: String { rawValue }
  }

  @ObservedObject var model: MacOSDesignModel
  @Environment(\.dismiss) private var dismiss
  @State private var provider = ""
  @State private var sourceMode = SourceMode.thisMac
  @State private var accountName = ""
  @State private var detectedIdentity = ""
  @State private var detectedSource = ""
  @State private var loginMethod = ""
  @State private var confirmClear = false
  @State private var isVerifying = false
  @State private var isDetecting = false
  @State private var credentialKind = ""
  @State private var credentialProfile = ""
  @State private var apiSecret = ""
  @State private var revealSecret = false
  /// Production save waits for the engine result (C4) instead of dismissing
  /// on accept: failures keep field context instead of arriving after close.
  @State private var awaitingSaveResult = false
  @State private var saveError: String?
  @State private var detectionTask: Task<Void, Never>?
  @State private var loginTask: Task<Void, Never>?
  @FocusState private var nameFocused: Bool

  private var providers: [DesignProviderOption] { model.savedAccountProviderOptions }
  private var selectedProvider: DesignProviderOption? { providers.first { $0.id == provider } }
  private var providerLabel: String { selectedProvider?.label ?? provider }
  private var loginMethods: [DesignLoginMethod] { selectedProvider?.loginMethods ?? [] }
  private var selectedLoginMethod: DesignLoginMethod? {
    loginMethods.first { $0.id == loginMethod }
  }
  /// Tencent's WorkBuddy/CodeBuddy endpoints are undocumented — the method
  /// carries provenance "community", so say so instead of "official".
  private var isCommunityFlow: Bool { selectedLoginMethod?.provenance == "community" }
  private var isWorkBuddyProvider: Bool {
    ["workbuddy", "codebuddy"].contains(provider.lowercased())
  }
  /// The WorkBuddy desktop app never shares its login (Electron safeStorage),
  /// so "On This Mac" only sees a previous AnyPick sign-in. When the app is
  /// present we say so explicitly instead of the flat "No login on this Mac".
  private var workBuddyAppInstalled: Bool {
    guard isWorkBuddyProvider else { return false }
    let home = FileManager.default.homeDirectoryForCurrentUser.path
    return [".workbuddy-ai", ".workbuddy", ".codebuddy"].contains {
      var isDir: ObjCBool = false
      return
        FileManager.default.fileExists(
          atPath: home + "/" + $0, isDirectory: &isDir) && isDir.boolValue
    }
  }
  private var isZaiProvider: Bool { provider.lowercased() == "zai" }
  /// The ZCode desktop app keeps its login in its Electron store — verified:
  /// no plaintext credential, no auth cookies, no plan token outside it —
  /// so AnyPick can only take a dashboard key, never import the app login.
  private var zcodeAppInstalled: Bool {
    guard isZaiProvider else { return false }
    var isDir: ObjCBool = false
    return FileManager.default.fileExists(
      atPath: FileManager.default.homeDirectoryForCurrentUser.path + "/.zcode",
      isDirectory: &isDir) && isDir.boolValue
  }
  private var zcodePlanWarning: String? {
    guard zcodeAppInstalled else { return nil }
    let url = URL(
      fileURLWithPath: FileManager.default.homeDirectoryForCurrentUser.path
        + "/.zcode/v2/coding-plan-cache.json")
    guard let data = try? Data(contentsOf: url) else { return nil }
    return zcodeCodingPlanWarning(data)
  }
  private var supportsNewAccountLogin: Bool { !loginMethods.isEmpty }
  /// Typed credential input for compatible vendors (API keys / plan keys).
  /// These providers have no live login to snapshot and no browser flow, so
  /// the sheet collects the key instead of offering On This Mac / New Account.
  private var credentialKinds: [DesignCredentialKind] { selectedProvider?.credentialKinds ?? [] }
  private var isKeyInputMode: Bool { !supportsNewAccountLogin && !credentialKinds.isEmpty }
  private var selectedCredentialKind: DesignCredentialKind? {
    credentialKinds.first { $0.kind == credentialKind } ?? credentialKinds.first
  }
  private var availableProfiles: [DesignCredentialProfile] {
    selectedCredentialKind?.profiles ?? []
  }
  private var selectedProfileID: String {
    if availableProfiles.contains(where: { $0.id == credentialProfile }) { return credentialProfile }
    return availableProfiles.first?.id ?? ""
  }

  private var currentLogin: DesignLiveLogin? {
    liveLogin(for: selectedProvider)
  }

  private var hasIdentity: Bool {
    !detectedIdentity.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
  }

  private var sourceAppName: String {
    currentLogin?.appName ?? selectedProvider?.label ?? anypickDisplayName(provider)
  }

  private var canSubmit: Bool {
    let blocked = isVerifying || isDetecting
    if isKeyInputMode {
      return addCredentialInputCanSubmit(
        name: accountName,
        secret: apiSecret,
        providerEnabled: selectedProvider?.enabled == true,
        isBlocked: blocked
      )
    }
    if sourceMode == .newAccount, !hasIdentity {
      return addSavedAccountCanSubmit(
        name: accountName,
        isNewAccountMode: true,
        hasIdentity: false,
        hasLiveLogin: false,
        methodAvailable: selectedLoginMethod?.available == true,
        providerEnabled: selectedProvider?.enabled == true,
        isBlocked: blocked
      )
    }
    return addSavedAccountCanSubmit(
      name: accountName,
      isNewAccountMode: sourceMode == .newAccount,
      hasIdentity: hasIdentity,
      hasLiveLogin: currentLogin != nil,
      methodAvailable: selectedLoginMethod?.available == true,
      providerEnabled: selectedProvider?.enabled == true,
      isBlocked: blocked
    )
  }

  /// Engine-provided reason shown inline when the primary action is disabled
  /// (C6), or when the sheet stranded with no usable provider (C10).
  private var signInBlockerMessage: String? {
    addAccountBlockerMessage(
      provider: selectedProvider,
      method: selectedLoginMethod,
      isNewAccountMode: sourceMode == .newAccount
    )
  }

  private var primaryTitle: String {
    if isDetecting || isVerifying { return "Checking" }
    if sourceMode == .newAccount, !hasIdentity { return "Sign In with \(providerLabel)" }
    return "Add Account"
  }

  private var autoDetectionKey: String {
    "\(sourceMode.rawValue):\(provider)"
  }

  var body: some View {
    DesignSheetHost(
      title: "Add Account",
      width: 440,
      primaryTitle: primaryTitle,
      primaryDisabled: !canSubmit || model.isBusy || awaitingSaveResult,
      primaryIdentifier: "account-flow.save",
      onCancel: cancelAndDismiss,
      onPrimary: submit
    ) {
      Form {
        Section {
          if supportsNewAccountLogin {
            Picker("Source", selection: $sourceMode) {
              ForEach(SourceMode.allCases) { mode in
                Text(mode.rawValue).tag(mode)
              }
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            .accessibilityIdentifier("account-flow.source")
          }

          LabeledContent("Provider") {
            DesignProviderPicker(options: providers, selection: $provider)
              .fixedSize()
              .accessibilityIdentifier("account-flow.provider")
          }

          if isKeyInputMode {
            keyInputRows
          } else {
            currentLoginRow
            if sourceMode == .newAccount, loginMethods.count > 1 {
              Picker("Sign-in method", selection: $loginMethod) {
                ForEach(loginMethods) { method in
                  Text(method.label).tag(method.id)
                }
              }
              .accessibilityIdentifier("account-flow.method")
              .help(selectedLoginMethod?.detail ?? "")
            }
            if let blocker = signInBlockerMessage {
              Text(blocker)
                .font(.footnote)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityIdentifier("account-flow.blocker")
            }
          }
          TextField("Account Name", text: $accountName, prompt: Text("Personal"))
            .focused($nameFocused)
            .accessibilityIdentifier("account-flow.name")
          if let saveError {
            Text(saveError)
              .font(.footnote)
              .foregroundStyle(.secondary)
              .fixedSize(horizontal: false, vertical: true)
              .accessibilityIdentifier("account-flow.error")
          }
        } header: {
          Text("Add Account")
        } footer: {
          Text(sourceFooter)
        }
      }
      .onAppear {
        selectInitialProvider()
        selectDefaultLoginMethod()
        selectDefaultCredential()
        syncCurrentLogin()
        if accountName.isEmpty {
          accountName = suggestedAccountName
        }
        nameFocused = true
      }
      .task(id: autoDetectionKey) {
        // Key-input vendors have no live login to detect: probing them only
        // produces a "no live login" error modal over the API-key form.
        guard sourceMode == .thisMac, !provider.isEmpty, !isKeyInputMode else { return }
        await Task.yield()
        guard !Task.isCancelled else { return }
        detectLogin()
      }
      .onChange(of: provider) { _, _ in
        detectedIdentity = ""
        detectedSource = ""
        isVerifying = false
        isDetecting = false
        apiSecret = ""
        revealSecret = false
        selectDefaultLoginMethod()
        selectDefaultCredential()
        if !supportsNewAccountLogin { sourceMode = .thisMac }
        if sourceMode == .thisMac { syncCurrentLogin() }
      }
      .onChange(of: sourceMode) { _, mode in
        isVerifying = false
        isDetecting = false
        if mode == .thisMac {
          syncCurrentLogin()
        } else {
          detectedIdentity = ""
          detectedSource = ""
        }
      }
      .onChange(of: model.operationResult) { _, result in
        if result?.operation == "account-login" {
          isVerifying = false
          if result?.succeeded == true { dismiss() }
        } else if result?.operation == "account-save-input" {
          isVerifying = false
          if result?.succeeded == true {
            dismiss()
          } else if isKeyInputMode {
            // Key saves used to fail silently (C4 hole): duplicate names and
            // rejected secrets now read inline instead of stranding the form.
            if let message = result?.message, !message.isEmpty {
              saveError = message
            } else {
              saveError = "Couldn’t save that key. Try again."
            }
          }
        } else if result?.operation == "account-save", awaitingSaveResult {
          awaitingSaveResult = false
          if result?.succeeded == true {
            dismiss()
          } else if let message = result?.message, !message.isEmpty {
            saveError = message
          } else {
            saveError = "Couldn’t save that account. Try again."
          }
        } else if result?.operation == "account-detect" {
          isDetecting = false
          if result?.succeeded == true {
            syncCurrentLogin(detectionMessage: result?.message)
          } else {
            detectedIdentity = ""
            detectedSource = ""
          }
        }
      }
      .onDisappear {
        detectionTask?.cancel()
        loginTask?.cancel()
        if isVerifying { model.cancelAccountLogin() }
      }
      .confirmationDialog(
        "Sign Out of \(sourceAppName)?",
        isPresented: $confirmClear,
        titleVisibility: .visible
      ) {
        Button("Sign Out", role: .destructive) {
          detectedIdentity = ""
          detectedSource = ""
          model.clearLiveLogin(provider: provider)
        }
        .accessibilityIdentifier("account-flow.sign-out")
        Button("Cancel", role: .cancel) {}
      } message: {
        Text(
          "AnyPick backs up this account before signing out on this Mac. Remote sessions stay active."
        )
      }
    }
  }

  private var detectedSourceText: String {
    if !detectedSource.isEmpty { return detectedSource }
    return sourceAppName
  }

  private var sourceFooter: String {
    if isKeyInputMode {
      return
        "AnyPick stores this key so supported apps can use \(providerLabel) through a local proxy."
    }
    if isDetecting { return "AnyPick will fill in the account when it appears." }
    if isVerifying || (sourceMode == .newAccount && !hasIdentity) {
      return "AnyPick saves the account when the browser flow finishes."
    }
    return "AnyPick saves this account so you can switch back anytime."
  }

  private var identityTitle: String {
    if isDetecting || isVerifying { return "Checking \(sourceAppName)" }
    if hasIdentity { return detectedIdentity }
    if sourceMode == .newAccount { return sourceAppName }
    return "No login on this Mac"
  }

  private var identityDetail: String {
    if isDetecting || isVerifying { return "This usually takes a few seconds." }
    if hasIdentity {
      if sourceMode == .thisMac { return "\(detectedSourceText) on this Mac" }
      return detectedSourceText
    }
    if sourceMode == .newAccount {
      return isCommunityFlow
        ? "Opens \(providerLabel)’s community-supported browser flow"
        : "Opens \(providerLabel)’s official browser flow"
    }
    // WorkBuddy has no shared login with its desktop app: AnyPick owns the
    // token end to end, so "On This Mac" only ever sees a previous AnyPick
    // sign-in — first-timers must use New Account.
    if isWorkBuddyProvider {
      return workBuddyAppInstalled
        ? "WorkBuddy app is on this Mac but keeps its own login — link it with New Account"
        : "WorkBuddy doesn’t share its app login — use New Account to sign in"
    }
    return sourceAppName
  }

  private var showsSignOutMenu: Bool {
    sourceMode == .thisMac && selectedProvider?.canClear == true && currentLogin != nil
  }

  private var checkAgainHelp: String {
    if sourceMode == .newAccount, !hasIdentity {
      return isCommunityFlow
        ? "Opens \(providerLabel)’s community-supported browser flow"
        : "Opens \(providerLabel)’s official browser flow"
    }
    return "Checks the \(sourceAppName) account currently active on this Mac."
  }

  private var currentLoginRow: some View {
    HStack(spacing: 10) {
      DesignProviderIcon(
        provider: provider,
        fallbackSymbol: DesignBrandAsset.fallbackSymbol(for: provider),
        size: 24
      )
      VStack(alignment: .leading, spacing: 2) {
        Text(identityTitle)
          .lineLimit(1)
        if !identityDetail.isEmpty {
          Text(identityDetail)
            .font(.subheadline)
            .foregroundStyle(.secondary)
            .lineLimit(1)
        }
      }
      .frame(maxWidth: .infinity, alignment: .leading)
      loginAccessories
    }
    .accessibilityElement(children: .contain)
    .accessibilityIdentifier("account-flow.identity")
  }

  private var keyInputRows: some View {
    Group {
      if credentialKinds.count > 1 {
        Picker("Credential", selection: $credentialKind) {
          ForEach(credentialKinds) { kind in
            Text(kind.label).tag(kind.kind)
          }
        }
        .accessibilityIdentifier("account-flow.credential-kind")
        .onChange(of: credentialKind) { _, _ in credentialProfile = "" }
      }
      if availableProfiles.count > 1 {
        Picker("Profile", selection: $credentialProfile) {
          ForEach(availableProfiles) { profile in
            Text(profile.label).tag(profile.id)
          }
        }
        .accessibilityIdentifier("account-flow.credential-profile")
      }
      LabeledContent(selectedCredentialKind?.label ?? "API Key") {
        HStack(spacing: 8) {
          Group {
            if revealSecret {
              TextField("", text: $apiSecret)
            } else {
              SecureField("", text: $apiSecret)
            }
          }
          .labelsHidden()
          .accessibilityIdentifier("account-flow.api-key")

          Button {
            revealSecret.toggle()
          } label: {
            Image(systemName: revealSecret ? "eye.slash" : "eye")
              .foregroundStyle(.secondary)
          }
          .buttonStyle(.plain)
          .accessibilityLabel(revealSecret ? "Hide API Key" : "Show API Key")
        }
      }
      if isZaiProvider {
        // Z.ai has no browser flow and the ZCode app login can't be
        // imported: say where the key comes from instead of a bare field.
        Text(
          zcodePlanWarning
            ?? "Paste the GLM Coding Plan key from the Z.ai dashboard (API Keys)."
        )
        .font(.footnote)
        .foregroundStyle(.secondary)
        .fixedSize(horizontal: false, vertical: true)
        .accessibilityIdentifier("account-flow.zcode-hint")
      }
    }
  }

  private var loginAccessories: some View {
    HStack(spacing: 4) {
      if isDetecting || isVerifying {
        ProgressView()
          .controlSize(.small)
          .accessibilityLabel("Checking")
      } else {
        Button {
          if sourceMode == .newAccount, !hasIdentity { signIn() } else { detectLogin() }
        } label: {
          Image(
            systemName: sourceMode == .newAccount && !hasIdentity
              ? "arrow.up.right.square"
              : "arrow.clockwise"
          )
        }
        .buttonStyle(.bordered)
        .controlSize(.small)
        .disabled(model.isBusy)
        .help(checkAgainHelp)
        .accessibilityLabel(
          sourceMode == .newAccount && !hasIdentity ? "Start Sign In" : "Check Again"
        )
        .accessibilityIdentifier("account-flow.detect")
      }
      if showsSignOutMenu {
        Menu {
          Button("Sign Out on This Mac", role: .destructive) { confirmClear = true }
        } label: {
          Image(systemName: "ellipsis.circle")
        }
        .menuStyle(.borderedButton)
        .controlSize(.small)
        .disabled(model.isBusy)
        .accessibilityLabel("Account actions")
        .accessibilityIdentifier("account-flow.clear")
      }
    }
  }

  private func syncCurrentLogin(detectionMessage: String? = nil) {
    if let login = currentLogin {
      detectedIdentity = login.identity ?? "Current login"
      detectedSource = login.appName
      return
    }
    detectedIdentity = accountIdentity(fromDetectionMessage: detectionMessage) ?? ""
    detectedSource = detectedIdentity.isEmpty ? "" : sourceAppName
  }

  private func liveLogin(for option: DesignProviderOption?) -> DesignLiveLogin? {
    guard let option else { return nil }
    return model.currentLiveLogins.first {
      $0.id.caseInsensitiveCompare(option.id) == .orderedSame
        || ($0.sourceID == nil
          && $0.providerID.caseInsensitiveCompare(option.id) == .orderedSame)
        || $0.providerName.caseInsensitiveCompare(option.label) == .orderedSame
    }
  }

  private func selectInitialProvider() {
    let enabledProviders = providers.filter(\.enabled)
    guard !enabledProviders.isEmpty else {
      provider = ""
      return
    }
    if let liveProvider = enabledProviders.first(where: { liveLogin(for: $0) != nil }) {
      provider = liveProvider.id
    } else if !enabledProviders.contains(where: { $0.id == provider }) {
      // Native sign-in first: key-only vendors sort before "claude", so the
      // raw first-enabled row would default the sheet to an API-key form.
      provider =
        enabledProviders.first(where: { !$0.loginMethods.isEmpty })?.id
        ?? enabledProviders[0].id
    }
  }

  private var suggestedAccountName: String {
    let existing = model.accounts.filter {
      $0.kind == .saved && $0.provider.caseInsensitiveCompare(provider) == .orderedSame
    }.map(\.name)
    if !existing.contains(where: { $0.caseInsensitiveCompare("Work") == .orderedSame }) {
      return "Work"
    }
    if !existing.contains(where: { $0.caseInsensitiveCompare("Personal") == .orderedSame }) {
      return "Personal"
    }
    return "Account \(existing.count + 1)"
  }

  private func detectLogin() {
    isDetecting = true
    if model.isProduction {
      if !model.detectSavedAccount(provider: provider) {
        isDetecting = false
      }
      return
    }
    detectionTask?.cancel()
    detectionTask = Task { @MainActor in
      try? await Task.sleep(nanoseconds: 650_000_000)
      guard !Task.isCancelled else { return }
      if let login = model.liveLogin(for: provider) {
        detectedIdentity = login.identity ?? ""
        detectedSource = login.appName
      } else {
        detectedIdentity = ""
        detectedSource = ""
      }
      isDetecting = false
    }
  }

  private func selectDefaultLoginMethod() {
    loginMethod =
      loginMethods.first(where: {
        $0.provenance == "official" && $0.preferred && $0.available
      })?.id
      ?? loginMethods.first(where: { $0.provenance == "official" && $0.available })?.id
      ?? loginMethods.first(where: { $0.preferred && $0.available })?.id
      ?? loginMethods.first(where: \.available)?.id
      ?? loginMethods.first?.id
      ?? ""
  }

  private func selectDefaultCredential() {
    if !credentialKinds.contains(where: { $0.kind == credentialKind }) {
      credentialKind = credentialKinds.first?.kind ?? ""
    }
    if !availableProfiles.contains(where: { $0.id == credentialProfile }) {
      credentialProfile = availableProfiles.first?.id ?? ""
    }
  }

  private func submit() {
    if isKeyInputMode {
      saveKey()
      return
    }
    if sourceMode == .newAccount, !hasIdentity {
      signIn()
      return
    }
    save()
  }

  private func saveKey() {
    guard let kind = selectedCredentialKind else { return }
    saveError = nil
    isVerifying = true
    if model.isProduction {
      if !model.saveCredentialInput(
        provider: provider,
        name: accountName,
        kind: kind.kind,
        profile: selectedProfileID,
        secret: apiSecret
      ) {
        isVerifying = false
      }
      return
    }
    isVerifying = false
    if model.saveCredentialInput(
      provider: provider,
      name: accountName,
      kind: kind.kind,
      profile: selectedProfileID,
      secret: apiSecret
    ) {
      dismiss()
    }
  }

  private func signIn() {
    guard let method = selectedLoginMethod else { return }
    isVerifying = true
    if model.isProduction {
      if !model.loginSavedAccount(
        provider: provider,
        name: accountName,
        label: "",
        methodId: method.id
      ) {
        isVerifying = false
      }
      return
    }
    loginTask?.cancel()
    loginTask = Task { @MainActor in
      try? await Task.sleep(nanoseconds: 800_000_000)
      guard !Task.isCancelled else { return }
      let identity =
        switch provider {
        case "Claude": "alex@personal.example"
        case "OpenAI": "codex@company.example"
        case "Gemini": "builder@company.example"
        default: "signed-in-\(provider.lowercased())"
        }
      model.upsertLiveLogin(provider: provider, identity: identity, appName: sourceAppName)
      let saved = model.loginSavedAccount(
        provider: provider,
        name: accountName,
        label: "",
        methodId: method.id
      )
      isVerifying = false
      if saved { dismiss() }
    }
  }

  private func cancelAndDismiss() {
    detectionTask?.cancel()
    loginTask?.cancel()
    if isVerifying { model.cancelAccountLogin() }
    isDetecting = false
    isVerifying = false
    dismiss()
  }

  private func save() {
    let identity =
      detectedIdentity.isEmpty
      ? "Current \(providerLabel) login"
      : detectedIdentity
    saveError = nil
    if model.isProduction {
      // Dismissal waits for the engine result (C4); the fixture answers
      // synchronously and dismisses at once below.
      if model.addSavedAccount(
        provider: provider,
        name: accountName,
        label: "",
        identity: identity
      ) {
        awaitingSaveResult = true
      }
      return
    }
    if model.addSavedAccount(
      provider: provider,
      name: accountName,
      label: "",
      identity: identity
    ) {
      dismiss()
    }
  }
}

struct AddGatewaySheet: View {
  @ObservedObject var model: MacOSDesignModel
  private let targetClientID: String?
  private let onCreated: () -> Void
  @Environment(\.dismiss) private var dismiss
  @State private var provider: String
  @State private var gatewayName = ""
  @State private var endpoint = ""
  @State private var apiKey = ""
  @State private var revealKey = false
  @State private var connectionState: ConnectionState = .untested
  @State private var connectionMessage = ""
  @State private var region: String?
  @FocusState private var focusedField: Field?

  private enum Field: Hashable { case name, endpoint, apiKey }
  private enum ConnectionState { case untested, testing, available, unavailable }
  private var providers: [DesignProviderOption] { model.gatewayProviderOptions }
  private var selectedProvider: DesignProviderOption? { providers.first { $0.id == provider } }
  private var inferredProvider: DesignProviderOption? {
    model.preferredGatewayProvider(for: targetClientID)
  }
  private var isAPIKeyAccount: Bool { selectedProvider?.kind == "account-api-key" }
  private var regionChoices: [String] { selectedProvider?.regions ?? [] }
  private var suppliedEndpoint: String? { model.defaultGatewayEndpoint(for: provider) }
  private var providerSuppliesEndpoint: Bool {
    selectedProvider?.providesEndpoint == true || suppliedEndpoint != nil
  }
  private var usesCustomEndpoint: Bool { !isAPIKeyAccount && !providerSuppliesEndpoint }
  private let fieldWidth: CGFloat = 220

  init(
    model: MacOSDesignModel,
    targetClientID: String? = nil,
    onCreated: @escaping () -> Void = {}
  ) {
    self.model = model
    self.targetClientID = targetClientID
    self.onCreated = onCreated
    let initial =
      model.preferredGatewayProvider(for: targetClientID)
      ?? model.gatewayProviderOptions.first
    _provider = State(initialValue: initial?.id ?? "")
  }

  private var resolvedEndpoint: String {
    suppliedEndpoint ?? (providerSuppliesEndpoint ? "" : endpoint)
  }

  private var canSave: Bool {
    !gatewayName.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
      && (requiresApiKey
        ? !apiKey.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        : true)
      && selectedProvider != nil
      && (usesCustomEndpoint
        ? !endpoint.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        : true)
      && (!isAPIKeyAccount || regionChoices.isEmpty || regionChoices.contains(region ?? ""))
  }

  private var requiresApiKey: Bool { selectedProvider?.requiresApiKey ?? true }

  var body: some View {
    DesignSheetHost(
      title: "Add API Gateway",
      width: 456,
      primaryTitle: "Add Gateway",
      primaryDisabled: !canSave || model.isBusy,
      primaryIdentifier: "gateway-flow.save",
      onCancel: { dismiss() },
      onPrimary: save
    ) {
      Form {
          Section {
            LabeledContent("Provider") {
              if let inferredProvider {
                Label {
                  Text(inferredProvider.label)
                } icon: {
                  DesignProviderIcon(
                    provider: inferredProvider.id,
                    fallbackSymbol: DesignBrandAsset.fallbackSymbol(for: inferredProvider.id),
                    size: 16
                  )
                }
                .fixedSize()
              } else {
                DesignProviderPicker(
                  options: providers,
                  selection: $provider,
                  accessibilityIdentifier: "gateway-flow.provider",
                  groupsProviders: true
                )
                .frame(width: 180)
              }
            }
            LabeledContent("Name") {
              TextField(
                "", text: $gatewayName,
                prompt: Text(usesCustomEndpoint ? "My Gateway" : "Production")
              )
              .labelsHidden()
              .frame(width: fieldWidth)
              .focused($focusedField, equals: .name)
              .accessibilityIdentifier("gateway-flow.name")
            }
          } header: {
            Text("Add API Gateway")
          }

          Section {
            if requiresApiKey {
              LabeledContent("API Key") {
                HStack(spacing: 8) {
                  Group {
                    if revealKey {
                      TextField("", text: $apiKey)
                    } else {
                      SecureField("", text: $apiKey)
                    }
                  }
                  .labelsHidden()
                  .focused($focusedField, equals: .apiKey)
                  .accessibilityIdentifier("gateway-flow.api-key")

                  Button {
                    revealKey.toggle()
                  } label: {
                    Image(systemName: revealKey ? "eye.slash" : "eye")
                      .foregroundStyle(.secondary)
                  }
                  .buttonStyle(.plain)
                  .accessibilityLabel(revealKey ? "Hide API Key" : "Show API Key")
                }
                .frame(width: fieldWidth)
              }
            } else {
              LabeledContent("Credentials") {
                Text("Uses your Grok login session (~/.grok/auth.json).")
                  .font(.footnote)
                  .foregroundStyle(.secondary)
                  .frame(width: fieldWidth, alignment: .leading)
              }
              LabeledContent("Setup") {
                Text("Run: grok login")
                  .font(.footnote)
                  .foregroundStyle(.secondary)
                  .frame(width: fieldWidth, alignment: .leading)
              }
            }
            if isAPIKeyAccount, !regionChoices.isEmpty {
              LabeledContent("Region") {
                Picker(
                  "",
                  selection: Binding(
                    get: {
                      region ?? selectedProvider?.defaultRegion ?? regionChoices.first ?? ""
                    },
                    set: { region = $0 }
                  )
                ) {
                  ForEach(regionChoices, id: \.self) { Text($0).tag($0) }
                }
                .labelsHidden()
                .frame(width: fieldWidth)
                .accessibilityIdentifier("gateway-flow.region")
              }
            } else if providerSuppliesEndpoint {
              LabeledContent(
                "Endpoint",
                value: suppliedEndpoint ?? "Provided by \(selectedProvider?.label ?? provider)"
              )
            } else {
              LabeledContent("Endpoint") {
                TextField("", text: $endpoint, prompt: Text("https://gateway.example/v1"))
                  .labelsHidden()
                  .frame(width: fieldWidth)
                  .focused($focusedField, equals: .endpoint)
                  .accessibilityIdentifier("gateway-flow.endpoint")
              }
            }
            if !isAPIKeyAccount {
              LabeledContent("Connection") {
                connectionControl
              }
            }
          } header: {
            Text("Credentials")
          } footer: {
            Text(connectionFooter)
          }
      }
      .onAppear {
        if let inferredProvider {
          provider = inferredProvider.id
        } else if !providers.contains(where: { $0.id == provider }) {
          provider = providers.first?.id ?? ""
        }
        endpoint = suppliedEndpoint ?? ""
        if gatewayName.isEmpty { gatewayName = suggestedGatewayName }
        syncRegion()
        focusedField = .name
      }
      .onChange(of: provider) {
        syncRegion()
        resetConnectionTest()
        endpoint = suppliedEndpoint ?? ""
      }
      .onChange(of: endpoint) { resetConnectionTest() }
      .onChange(of: apiKey) { resetConnectionTest() }
      .onChange(of: model.operationResult) { _, result in
        guard result?.operation == "gateway-test", connectionState == .testing else { return }
        connectionState = result?.succeeded == true ? .available : .unavailable
        connectionMessage = result?.message ?? ""
      }
    }
  }

  @ViewBuilder
  private var connectionControl: some View {
    switch connectionState {
    case .testing:
      HStack(spacing: 6) {
        ProgressView()
          .controlSize(.small)
          .accessibilityLabel("Testing connection")
        Button("Test Connection", action: {})
          .disabled(true)
      }
      .accessibilityIdentifier("gateway-flow.testing")
    case .available:
      HStack(spacing: 10) {
        Label("Connected", systemImage: "checkmark.circle.fill")
          .foregroundStyle(.green)
        Button("Test Again", action: testConnection)
      }
      .accessibilityIdentifier("gateway-flow.available")
    case .unavailable:
      HStack(spacing: 10) {
        Label("Not Connected", systemImage: "exclamationmark.circle.fill")
          .foregroundStyle(.red)
        Button("Try Again", action: testConnection)
      }
      .accessibilityIdentifier("gateway-flow.unavailable")
    case .untested:
      Button("Test Connection", action: testConnection)
        .disabled(
          !gatewayConnectionTestEnabled(
            requiresApiKey: requiresApiKey, apiKey: apiKey, isBusy: model.isBusy)
        )
        .accessibilityIdentifier("gateway-flow.test")
    }
  }

  private var connectionFooter: String {
    switch connectionState {
    case .testing:
      return resolvedEndpoint.isEmpty
        ? "Contacting \(selectedProvider?.label ?? provider)…"
        : "Contacting \(resolvedEndpoint)…"
    case .available:
      return connectionMessage.isEmpty
        ? "The gateway returned a live model catalog." : connectionMessage
    case .unavailable:
      return connectionMessage.isEmpty
        ? "The gateway did not return a live model catalog. Check the URL and API key."
        : connectionMessage
    case .untested:
      if isAPIKeyAccount {
        return "AnyPick stores this key in the selected provider’s account store."
      }
      if !usesCustomEndpoint {
        return
          "\(selectedProvider?.label ?? provider) supplies the endpoint. Choose Custom to enter a different URL."
      }
      return "AnyPick detects the API format automatically."
    }
  }

  private func testConnection() {
    guard connectionState != .testing else { return }
    connectionState = .testing
    connectionMessage = ""
    let accepted = model.testGateway(
      provider: provider,
      endpoint: resolvedEndpoint,
      apiKey: apiKey
    )
    if !model.isProduction {
      connectionState = accepted ? .available : .unavailable
      connectionMessage =
        accepted
        ? "Connection available in the design fixture."
        : "The design fixture rejected this API key."
    } else if !accepted {
      connectionState = .unavailable
      connectionMessage = "AnyPick could not start the connection test."
    }
  }

  private func resetConnectionTest() {
    connectionState = .untested
    connectionMessage = ""
  }

  private func save() {
    if model.addGateway(
      provider: provider,
      name: gatewayName,
      label: "",
      endpoint: resolvedEndpoint,
      apiKey: apiKey,
      region: region,
      defaultModel: ""
    ) {
      onCreated()
      dismiss()
    }
  }

  private var suggestedGatewayName: String {
    if !model.accounts.contains(where: {
      $0.name.caseInsensitiveCompare("Production") == .orderedSame
    }) {
      return "Production"
    }
    let base = selectedProvider?.label ?? "Gateway"
    var suffix = 2
    while model.accounts.contains(where: {
      $0.name.caseInsensitiveCompare("\(base) \(suffix)") == .orderedSame
    }) {
      suffix += 1
    }
    return "\(base) \(suffix)"
  }

  private func syncRegion() {
    guard isAPIKeyAccount else {
      region = nil
      return
    }
    if !regionChoices.contains(region ?? "") {
      region = selectedProvider?.defaultRegion ?? regionChoices.first
    }
  }
}

struct AccountInspectSheet: View {
  @ObservedObject var model: MacOSDesignModel
  let accountID: String
  @Environment(\.dismiss) private var dismiss
  @State private var editing = false

  private var account: DesignAccount? { model.accounts.first { $0.id == accountID } }
  private var usageWindows: [DesignUsageWindow] {
    guard let account else { return [] }
    return model.usageWindows(for: account)
  }

  var body: some View {
    DesignSheetHost(
      title: account?.name ?? "Account",
      width: 440,
      primaryTitle: "Done",
      primaryIdentifier: "account-inspect.done",
      showsCancel: false,
      onCancel: { dismiss() },
      onPrimary: { dismiss() }
    ) {
      Form {
        if let account {
          Section {
            Label {
              VStack(alignment: .leading, spacing: 2) {
                Text(disambiguatedAccountName(for: account, in: model.accounts))
                let identity = accountIdentityLine(provider: account.provider, detail: account.detail)
                if !identity.isEmpty {
                  Text(identity)
                    .foregroundStyle(.secondary)
                }
              }
            } icon: {
              DesignProviderIcon(provider: account.provider, fallbackSymbol: account.symbol)
            }
            .accessibilityElement(children: .combine)
            .accessibilityLabel(accountAccessibilityLabel(for: account, in: model.accounts))
          }

          Section {
            LabeledContent("Provider", value: account.provider)
            LabeledContent("Type", value: account.kind == .gateway ? "API Gateway" : "Saved Login")
            LabeledContent("Status") {
              if model.isCurrentAccount(account) {
                Text("Current")
                  .foregroundStyle(.secondary)
              } else {
                DesignStatusLabel(status: account.status)
              }
            }
            Button("Edit…") { editing = true }
              .accessibilityIdentifier("account-inspect.edit")
          }

          if !usageWindows.isEmpty {
            Section {
              ForEach(usageWindows) { window in
                VStack(alignment: .leading, spacing: 6) {
                  LabeledContent(window.label) {
                    Text("\(window.remainingPercent)% remaining")
                      .foregroundStyle(.secondary)
                      .monospacedDigit()
                  }
                  ProgressView(value: Double(window.remainingPercent), total: 100)
                }
                .accessibilityIdentifier("account.usage.\(window.id)")
              }
            } header: {
              Text("Remaining Quotas")
            } footer: {
              Text(
                "Reported by \(account.provider) for the account currently active on this Mac."
              )
            }
          }
        }
      }
    }
    .sheet(isPresented: $editing) {
      EditAccountSheet(model: model, accountID: accountID)
    }
  }
}

struct EditAccountSheet: View {
  @ObservedObject var model: MacOSDesignModel
  let accountID: String
  @Environment(\.dismiss) private var dismiss
  @State private var name = ""
  /// Production edit waits for the engine result (C4) like the add sheet.
  @State private var awaitingSaveResult = false
  @State private var saveError: String?
  @FocusState private var nameFocused: Bool

  private var account: DesignAccount? { model.accounts.first { $0.id == accountID } }

  private var canSave: Bool {
    let cleaned = name.trimmingCharacters(in: .whitespacesAndNewlines)
    return !cleaned.isEmpty && cleaned != account?.name
  }

  var body: some View {
    DesignSheetHost(
      title: account.map { "Edit \($0.name)" } ?? "Edit Account",
      width: 440,
      primaryTitle: "Save",
      primaryDisabled: !canSave || model.isBusy || awaitingSaveResult,
      primaryIdentifier: "account-edit.save",
      onCancel: { dismiss() },
      onPrimary: save
    ) {
      Form {
        Section {
          TextField("Name", text: $name)
            .focused($nameFocused)
            .accessibilityIdentifier("account-edit.name")
          if let account {
            LabeledContent("Provider", value: account.provider)
            LabeledContent("Type", value: account.kind.rawValue)
          }
          if let saveError {
            Text(saveError)
              .font(.footnote)
              .foregroundStyle(.secondary)
              .fixedSize(horizontal: false, vertical: true)
              .accessibilityIdentifier("account-edit.error")
          }
        } footer: {
          Text("The name is shown in AnyPick. The provider login is unchanged.")
        }
      }
      .onAppear {
        name = account?.name ?? ""
        nameFocused = true
      }
      .onChange(of: model.operationResult) { _, result in
        guard awaitingSaveResult,
          result?.operation == "account-edit" || result?.operation == "gateway-edit"
        else { return }
        awaitingSaveResult = false
        if result?.succeeded == true {
          dismiss()
        } else if let message = result?.message, !message.isEmpty {
          saveError = message
        } else {
          saveError = "Couldn’t save that name. Try again."
        }
      }
    }
  }

  private func save() {
    saveError = nil
    if model.isProduction {
      // No mutation fires for a vanished account — never strand the sheet
      // waiting for a result that cannot arrive.
      guard model.accounts.contains(where: { $0.id == accountID }) else {
        saveError = "That account is no longer available."
        return
      }
      // The store refuses while another mutation is in flight: only arm the
      // wait when the request was accepted, or the sheet hangs in a saving
      // state with no result ever arriving (A1). Matches AppModelsSheet.
      guard !model.isBusy else {
        saveError = "Another change is in progress. Try again in a moment."
        return
      }
      if model.updateAccount(id: accountID, label: name) {
        awaitingSaveResult = true
      } else {
        saveError = "Another change is in progress. Try again in a moment."
      }
      return
    }
    model.updateAccount(id: accountID, label: name)
    dismiss()
  }
}

struct ConnectGatewaySheet: View {
  @ObservedObject var model: MacOSDesignModel
  let gatewayID: String
  @Environment(\.dismiss) private var dismiss
  @State private var selectedAppIDs = Set<String>()

  private var gateway: DesignAccount? {
    model.accounts.first { $0.id == gatewayID && $0.kind == .gateway }
  }

  private var compatibleApps: [DesignApp] {
    guard let gateway else { return [] }
    return model.appsCompatible(with: gateway)
  }

  var body: some View {
    DesignSheetHost(
      title: gateway.map { "Connect \($0.name)" } ?? "Connect API Gateway",
      width: 440,
      primaryTitle: "Connect",
      primaryDisabled: model.isBusy || compatibleApps.isEmpty,
      primaryIdentifier: "gateway-connect.save",
      onCancel: { dismiss() },
      onPrimary: connect
    ) {
      Form {
        if compatibleApps.isEmpty {
          Section {
            Text("This gateway can be used with Claude Code, Codex, or OpenCode.")
              .foregroundStyle(.secondary)
          }
        } else {
          Section {
            ForEach(compatibleApps) { app in
              Toggle(isOn: binding(for: app.id)) {
                Label {
                  VStack(alignment: .leading, spacing: 2) {
                    Text(app.name)
                    Text(usingDetail(app))
                      .foregroundStyle(.secondary)
                  }
                } icon: {
                  DesignProviderIcon(provider: app.id, fallbackSymbol: app.symbol, size: 16)
                }
              }
              .accessibilityIdentifier("gateway-connect.app.\(app.id)")
            }
          } header: {
            Text("Apps")
          } footer: {
            Text("Choose which apps use this gateway.")
          }
        }
      }
      .onAppear(perform: hydrateSelection)
    }
  }

  private func binding(for appID: String) -> Binding<Bool> {
    Binding(
      get: { selectedAppIDs.contains(appID) },
      set: { isOn in
        if isOn { selectedAppIDs.insert(appID) } else { selectedAppIDs.remove(appID) }
      }
    )
  }

  private func usingDetail(_ app: DesignApp) -> String {
    guard let gateway, let route = model.gatewayRoute(in: app, matching: gateway) else {
      return app.detail
    }
    return app.selectedRouteID == route.id ? "Uses this gateway" : app.detail
  }

  private func hydrateSelection() {
    guard let gateway else { return }
    selectedAppIDs = Set(
      compatibleApps.compactMap { app in
        guard let route = model.gatewayRoute(in: app, matching: gateway) else { return nil }
        return app.selectedRouteID == route.id ? app.id : nil
      }
    )
  }

  private func connect() {
    model.connectGateway(id: gatewayID, appIDs: selectedAppIDs)
    dismiss()
  }
}
