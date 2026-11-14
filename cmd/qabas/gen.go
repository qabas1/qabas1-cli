package main

// Windows resources (icon, version info, manifest) for qabas.exe. Release builds
// run packaging/windows/build.ps1, which also stamps the release version.
//go:generate go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo@v1.4.1 -64=true -o=resource_windows_amd64.syso versioninfo.json
//go:generate go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo@v1.4.1 -arm=true -64=true -o=resource_windows_arm64.syso versioninfo.json
