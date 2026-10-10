# Homebrew distribution

The release workflow publishes the CLI formula and the macOS app cask to
`outof0/homebrew-tap`. Configure these repository settings before running a
release:

- `HOMEBREW_TAP_REPOSITORY=outof0/homebrew-tap`;
- `HOMEBREW_TAP_TOKEN` with permission to push that repository.

Users install the CLI or desktop app from the same tap:

```bash
brew install outof0/tap/anypick
brew install --cask outof0/tap/anypick
```

The formula installs the portable Go CLI for macOS and Linux. The cask installs
the universal macOS app from the GitHub Release. The app is not notarized, so
macOS may block its first launch. Right-click `AnyPick.app` and choose **Open**,
or go to **System Settings → Privacy & Security → Open Anyway**. You can also
remove the quarantine flag with:

```bash
xattr -dr com.apple.quarantine /Applications/AnyPick.app
```
