# Build branded qabas MSIs (x64 + arm64) with WiX v5.
#   pwsh packaging/windows/build-msi.ps1 -Version 1.2.3
# Expects dist/qabas_<Version>_windows_<arch>.exe (from build.ps1, ideally already signed).
# Outputs dist/qabas_<Version>_windows_<arch>.msi
[CmdletBinding()]
param(
    [string]$Version = $(if ($env:VERSION) { $env:VERSION } else { "dev" }),
    [string]$Dir = "dist",
    [string[]]$Arch = @("amd64", "arm64"),
    [string]$WixVersion = "5.0.2"
)

$ErrorActionPreference = "Stop"
$Root = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
$Template = Join-Path $PSScriptRoot "qabas.wxs"
$Assets = Join-Path $PSScriptRoot "assets"
$DistDir = Join-Path $Root $Dir

# MSI ProductVersion must be numeric; channel builds such as "dev" map to 0.0.1.
$productVersion = "0.0.1"
if ($Version -match '^v?(\d+)\.(\d+)\.(\d+)') {
    $productVersion = "$($Matches[1]).$($Matches[2]).$($Matches[3])"
}

$wix = Get-Command wix -ErrorAction SilentlyContinue
if (-not $wix -or -not ((& wix --version) -like "$WixVersion*")) {
    & dotnet tool install --global wix --version $WixVersion
    if ($LASTEXITCODE -ne 0) { & dotnet tool update --global wix --version $WixVersion }
    if ($LASTEXITCODE -ne 0) { throw "failed to install WiX $WixVersion (dotnet tool)" }
    $env:Path = "$env:Path;$(Join-Path $env:USERPROFILE '.dotnet\tools')"
}
& wix extension add -g "WixToolset.UI.wixext/$WixVersion"
if ($LASTEXITCODE -ne 0) { throw "failed to add WixToolset.UI.wixext/$WixVersion" }

foreach ($a in $Arch) {
    $wixArch = switch ($a) {
        "amd64" { "x64" }
        "arm64" { "arm64" }
        default { throw "unsupported arch: $a" }
    }
    $exe = Join-Path $DistDir "qabas_${Version}_windows_$a.exe"
    if (-not (Test-Path -LiteralPath $exe)) { throw "missing $exe; run packaging/windows/build.ps1 first" }
    $msi = Join-Path $DistDir "qabas_${Version}_windows_$a.msi"

    & wix build $Template -arch $wixArch -ext WixToolset.UI.wixext `
        -d "Version=$productVersion" -d "ExeSource=$exe" -d "AssetsDir=$Assets" `
        -o $msi
    if ($LASTEXITCODE -ne 0) { throw "wix build failed for $a" }
    Remove-Item -LiteralPath ([IO.Path]::ChangeExtension($msi, ".wixpdb")) -ErrorAction SilentlyContinue
    Write-Host "built $msi"
}
