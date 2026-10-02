# Distribution and release operations

`.github/workflows/release.yml` is the single release pipeline. It builds one
Go CLI archive per supported target, bundles the Tauri app for Linux/Windows,
signs and notarizes the Swift app, writes `SHA256SUMS.txt`, attaches an SBOM,
and publishes a GitHub Release only after all in-scope jobs pass.

## Artifact matrix

| Channel | Artifacts | Install surface |
| --- | --- | --- |
| GitHub Release | macOS DMG/ZIP, Linux AppImage/deb/rpm, Windows MSI/NSIS, CLI archives | direct download |
| npm | thin launcher; downloads a verified CLI archive on first run | `npm install --global anypick` |
| Homebrew | formula for CLI, cask for macOS app | external `homebrew-*` tap |
| WinGet | generated three-file manifest bundle | submit to `microsoft/winget-pkgs` |
| Scoop | generated `anypick.json` for x64/arm64 | external Scoop bucket |

## One-time repository setup

- macOS signing/notarization secrets: `APPLE_DEVELOPER_ID_APPLICATION`,
  `APPLE_CERT_P12_BASE64`, `APPLE_CERT_PASSWORD`, `APPLE_API_KEY_P8`,
  `APPLE_API_KEY_ID`, and `APPLE_API_ISSUER_ID`.
- npm trusted publishing for package `anypick`: repository
  `outof0/anypick`, workflow `.github/workflows/release.yml`, environment
  `production`. No npm token is needed; the workflow uses GitHub OIDC.
- Optional Homebrew tap: `HOMEBREW_TAP_REPOSITORY` variable and
  `HOMEBREW_TAP_TOKEN` secret.
- Optional Scoop bucket: `SCOOP_BUCKET_REPOSITORY` variable and
  `SCOOP_BUCKET_TOKEN` secret.
- Existing docs deployment credentials remain required by the release job.

Run the workflow from `main` with the default `release-scope=all` for a full
release: signed macOS app, Linux/Windows installers, and the cross-platform CLI
archives. Before staging, `packaging/smoke/linux-payload.sh` and
`packaging/smoke/windows-payload.ps1` extract each installer and run the
bundled Go helper; the GUI is not launched. `release-scope=macos-only` narrows
a release to macOS plus the CLI archives. Enable `publish-npm`,
`publish-homebrew`, or `publish-scoop` only after that channel's one-time setup
is complete. WinGet manifests are attached to the release for review and
submission because the Microsoft community repository owns final acceptance.
