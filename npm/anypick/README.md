# AnyPick npm launcher

This package is a thin launcher for the released AnyPick Go CLI. It downloads
the matching, checksum-verified CLI archive on first use and then forwards
arguments, standard I/O, signals, and the exit status to that binary.

It does not contain a Node service, a second implementation, or a plugin
runtime. Native desktop applications remain Node-free.

```bash
npm install --global anypick
anypick --help
```

The supported npm targets are macOS (Apple silicon and Intel), Linux (x64 and
arm64), and Windows (x64 and arm64). The downloaded CLI is cached under the
platform cache directory. Set `ANYPICK_CACHE_DIR` to choose another cache, or
`ANYPICK_BIN` to run a locally built Go binary.

Releases use npm trusted publishing from `.github/workflows/release.yml`. The
npm package settings must point at `outof0/anypick`, workflow file
`.github/workflows/release.yml`, and the `production` environment before the
workflow's `publish-npm` input is enabled.
