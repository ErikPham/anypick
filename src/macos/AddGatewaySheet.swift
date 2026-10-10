import SwiftUI

struct AddGatewaySheet: View {
  @ObservedObject var model: MacOSDesignModel
  private let targetClientID: String?
  private let onCreated: () -> Void
  @Environment(\.dismiss) private var dismiss
  @State private var provider: String
  @State private var gatewayName = ""
  @State private var endpoint = ""
  @State private var apiKey = ""
  @State private var gatewayID = ""
  @State private var accountID = ""
  @State private var revealKey = false
  @State private var connectionState: ConnectionState = .untested
  @State private var connectionMessage = ""
  @State private var region: String?
  @FocusState private var focusedField: Field?

  private enum Field: Hashable { case name, endpoint, apiKey, accountID, gatewayID }
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
    let isErrorFlow = ProcessInfo.processInfo.environment["ANYPICK_MACOS_FLOW"] == "add-gateway-error"
    _connectionState = State(initialValue: isErrorFlow ? .unavailable : .untested)
    _connectionMessage = State(
      initialValue: isErrorFlow
        ? "endpoint unreachable: could not connect to the gateway server. Verify the endpoint URL is spelled correctly and includes https://."
        : ""
    )
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
      && (!requiresAccountId
        || !accountID.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
      && (!isAPIKeyAccount || regionChoices.isEmpty || regionChoices.contains(region ?? ""))
  }

  private var requiresApiKey: Bool { selectedProvider?.requiresApiKey ?? true }
  private var requiresAccountId: Bool { selectedProvider?.requiresAccountId == true }

  var body: some View {
    DesignSheetHost(
      title: "Add API Gateway",
      width: 456,
      primaryTitle: "Add Gateway",
      primaryDisabled: !canSave || model.isBusy,
      primaryIdentifier: "gateway-flow.save",
      primaryHelp: "Save this gateway with the provider details shown below.",
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
                .help("Choose the service that issued this gateway’s credentials.")
              }
            }
            LabeledContent("Name") {
              TextField(
                "", text: $gatewayName,
                prompt: Text(usesCustomEndpoint ? "My Gateway" : "Production")
              )
              .labelsHidden()
              .focused($focusedField, equals: .name)
              .help("A short label to recognize this gateway in Accounts & APIs.")
              .accessibilityIdentifier("gateway-flow.name")
            }
          } footer: {
            Text(
              "Choose a provider to show only the fields it needs. "
                + "This name is how the gateway appears in Accounts & APIs."
            )
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
                  .help("Enter the API key issued by this provider.")
                  .accessibilityIdentifier("gateway-flow.api-key")

                  DesignIconButton(
                    symbol: revealKey ? "eye.slash" : "eye",
                    label: revealKey ? "Hide API Key" : "Show API Key",
                    identifier: "gateway-flow.toggle-key-visibility"
                  ) {
                    revealKey.toggle()
                  }
                }
              }
            } else {
              LabeledContent("Credentials") {
                Text("Uses your Grok login session (~/.grok/auth.json).")
                  .font(.footnote)
                  .foregroundStyle(.secondary)
              }
              LabeledContent("Setup") {
                Text("Run: grok login")
                  .font(.footnote)
                  .foregroundStyle(.secondary)
              }
            }
            if requiresAccountId {
              LabeledContent("Account ID") {
                TextField("", text: $accountID, prompt: Text("32-character hex account id"))
                  .labelsHidden()
                  .focused($focusedField, equals: .accountID)
                  .accessibilityIdentifier("gateway-flow.account-id")
                  .textContentType(.none)
                  .autocorrectionDisabled()
                  .help("Enter the account ID shown in this provider’s dashboard.")
              }
            }
            if selectedProvider?.requiresGatewayId == true {
              LabeledContent("Gateway ID") {
                TextField("", text: $gatewayID, prompt: Text("default"))
                  .labelsHidden()
                  .accessibilityIdentifier("gateway-flow.gateway-id")
                  .help("Enter the AI Gateway ID or name shown by this provider.")
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
                .accessibilityIdentifier("gateway-flow.region")
                .help("Choose the region where this provider account is configured.")
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
                  .focused($focusedField, equals: .endpoint)
                  .help("Enter the provider’s base URL, including https:// and any required path.")
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
        if ProcessInfo.processInfo.environment["ANYPICK_MACOS_FLOW"] == "add-gateway-error" {
          DispatchQueue.main.asyncAfter(deadline: .now() + 0.1) {
            connectionState = .unavailable
            connectionMessage = "endpoint unreachable: could not connect to the gateway server. Verify the endpoint URL is spelled correctly and includes https://."
          }
        }
      }
      .onChange(of: provider) {
        syncRegion()
        resetConnectionTest()
        endpoint = suppliedEndpoint ?? ""
      }
      .onChange(of: endpoint) { resetConnectionTest() }
      .onChange(of: apiKey) { resetConnectionTest() }
      .onChange(of: accountID) { resetConnectionTest() }
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
          .buttonStyle(.bordered)
          .controlSize(.small)
          .help("The connection test is already running.")
          .disabled(true)
      }
      .accessibilityIdentifier("gateway-flow.testing")
    case .available:
      HStack(spacing: 10) {
        Label("Connected", systemImage: "checkmark.circle.fill")
          .foregroundStyle(.green)
        Button("Test Again", action: testConnection)
          .buttonStyle(.bordered)
          .controlSize(.small)
          .help("Test again using the current provider details.")
      }
      .accessibilityIdentifier("gateway-flow.available")
    case .unavailable:
      HStack(spacing: 10) {
        Label("Not Connected", systemImage: "exclamationmark.circle.fill")
          .foregroundStyle(.red)
        Button("Try Again", action: testConnection)
          .buttonStyle(.bordered)
          .controlSize(.small)
          .help("Retry the connection test using the current details.")
      }
      .accessibilityIdentifier("gateway-flow.unavailable")
    case .untested:
      Button("Test Connection", action: testConnection)
        .buttonStyle(.bordered)
        .controlSize(.small)
        .help("Check whether this gateway can connect with the current details.")
        .disabled(
          !gatewayConnectionTestEnabled(
            requiresApiKey: requiresApiKey, apiKey: apiKey, isBusy: model.isBusy,
            requiresAccountId: requiresAccountId, accountID: accountID)
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
        ? "Connected successfully. The gateway returned a live model catalog." : connectionMessage
    case .unavailable:
      return connectionMessage.isEmpty
        ? "Could not connect to the gateway. Please check the endpoint URL and API key."
        : connectionMessage
    case .untested:
      if isAPIKeyAccount {
        return "AnyPick stores this key in the selected provider’s account store."
      }
      if requiresAccountId {
        return "The endpoint is derived from your Account ID."
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
      apiKey: apiKey,
      profile: requiresAccountId ? accountID.trimmingCharacters(in: .whitespacesAndNewlines) : nil
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
      gatewayId: gatewayID.trimmingCharacters(in: .whitespacesAndNewlines),
      profile: requiresAccountId ? accountID.trimmingCharacters(in: .whitespacesAndNewlines) : nil,
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
