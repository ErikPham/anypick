# Scoop distribution

The release workflow emits a Scoop manifest from the Windows CLI archives and
can publish it independently of the Windows desktop MSI. Put `anypick.json` in
a public Scoop bucket to make this install path available:

```powershell
scoop bucket add outof0 https://github.com/outof0/scoop-bucket
scoop install anypick
```

The manifest contains SHA-256 hashes for Windows x64 and arm64 archives.
