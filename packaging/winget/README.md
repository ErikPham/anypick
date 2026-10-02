# Windows Package Manager distribution

The release workflow includes a versioned WinGet manifest bundle whenever the
Windows MSI is in scope. Submit the three YAML files in the bundle to the
Microsoft `winget-pkgs` repository, or enable the repository's approved
automation once the package identifier has been accepted.

The installer URL is a stable, checksum-pinned GitHub Release asset:
`AnyPick-<version>-windows-x64.msi`.
