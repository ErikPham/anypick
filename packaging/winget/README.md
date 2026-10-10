# Windows Package Manager distribution

The release workflow includes a versioned WinGet manifest bundle whenever the
Windows desktop MSI is in scope. Submit the three YAML files in the bundle as
a PR to Microsoft's `winget-pkgs` repository and follow its automated checks
and review. WinGet acceptance is not part of the GitHub release gate.

The installer URL is a stable, checksum-pinned GitHub Release asset:
`AnyPick-<version>-windows-x64.msi`.
