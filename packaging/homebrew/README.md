# Homebrew distribution

The release workflow renders an AnyPick formula and macOS cask into a separate
tap repository. Configure these repository settings before selecting the
`publish-homebrew` release input:

- `HOMEBREW_TAP_REPOSITORY` variable, normally `outof0/homebrew-tap`;
- `HOMEBREW_TAP_TOKEN` secret with permission to push that repository;
- `HOMEBREW_PUBLISH_ENABLED=true` if releases should publish the tap without a
  manual workflow input.

Users then install either the Go CLI or the native app:

```bash
brew tap outof0/tap
brew install outof0/tap/anypick
brew install --cask outof0/tap/anypick
```

The tap must be a public repository named `homebrew-*`. The formula uses the
released CLI archives for macOS and Linux; the cask installs the signed,
notarized universal `AnyPick.app` archive.
