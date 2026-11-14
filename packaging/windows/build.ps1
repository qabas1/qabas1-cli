# Build qabas.exe for Windows (amd64 + arm64) with embedded icon, version info, and manifest.
#   pwsh packaging/windows/build.ps1 -Version 1.2.3 -Commit <sha>
# Outputs dist/qabas_<Version>_windows_<arch>.exe
[CmdletBinding()]
param(
    [string]$Version = $(if ($env:VERSION) { $env:VERSION } else { "dev" }),
    [string]$Commit = $(if ($env:COMMIT) { $env:COMMIT } else { "none" }),
    [string]$OutDir = "dist",
    [string[]]$Arch = @("amd64", "arm64")
)

$ErrorActionPreference = "Stop"
$Root = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
$Module = "github.com/qabasone/qabasone-cli"
$GoVersionInfo = "github.com/josephspurrier/goversioninfo/cmd/goversioninfo@v1.4.1"

$major = 0; $minor = 0; $patch = 0
if ($Version -match '^v?(\d+)\.(\d+)\.(\d+)') {
    $major = [int]$Matches[1]; $minor = [int]$Matches[2]; $patch = [int]$Matches[3]
}
$numeric = "$major.$minor.$patch.0"

$outPath = Join-Path $Root $OutDir
New-Item -ItemType Directory -Force -Path $outPath | Out-Null
$ldflags = "-s -w -X $Module/internal/version.Version=$Version -X $Module/internal/version.Commit=$Commit"

$CmdDir = Join-Path $Root "cmd\qabas"
$Config = Join-Path $CmdDir "versioninfo.json"
$TmpDir = if ($env:RUNNER_TEMP) { $env:RUNNER_TEMP } else { [IO.Path]::GetTempPath() }

& go install $GoVersionInfo
if ($LASTEXITCODE -ne 0) { throw "go install $GoVersionInfo failed" }
$GoBin = (& go env GOBIN).Trim()
if (-not $GoBin) { $GoBin = Join-Path ((& go env GOPATH).Trim() -split ';')[0] "bin" }
$Gvi = Join-Path $GoBin "goversioninfo.exe"
if (-not (Test-Path -LiteralPath $Gvi)) { throw "goversioninfo not found at $Gvi" }

$vi = Get-Content -LiteralPath $Config -Raw | ConvertFrom-Json
foreach ($field in "FileVersion", "ProductVersion") {
    $vi.FixedFileInfo.$field.Major = $major
    $vi.FixedFileInfo.$field.Minor = $minor
    $vi.FixedFileInfo.$field.Patch = $patch
    $vi.FixedFileInfo.$field.Build = 0
}
$vi.StringFileInfo.FileVersion = $numeric
$vi.StringFileInfo.ProductVersion = $Version
$vi.IconPath = (Resolve-Path (Join-Path $CmdDir $vi.IconPath)).Path
$vi.ManifestPath = (Resolve-Path (Join-Path $CmdDir $vi.ManifestPath)).Path
$stampedJson = $vi | ConvertTo-Json -Depth 5

Push-Location $CmdDir
try {
    foreach ($a in $Arch) {
        if ($a -notin "amd64", "arm64") { throw "unsupported arch: $a" }

        $stamped = Join-Path $TmpDir "versioninfo.$a.json"
        [IO.File]::WriteAllText($stamped, $stampedJson, [Text.UTF8Encoding]::new($false))
        if ((Get-Item -LiteralPath $stamped).Length -eq 0) { throw "stamped versioninfo is empty: $stamped" }
        $null = Get-Content -LiteralPath $stamped -Raw | ConvertFrom-Json

        $syso = Join-Path $CmdDir "resource_windows_$a.syso"
        Remove-Item -LiteralPath $syso -ErrorAction SilentlyContinue

        [string[]]$viArgs = @()
        if ($a -eq "arm64") { $viArgs += "-arm=true" }
        $viArgs += "-64=true", "-o=$syso", $stamped
        Write-Host "goversioninfo $($viArgs -join ' ')"
        & $Gvi @viArgs
        if ($LASTEXITCODE -ne 0) { throw "goversioninfo failed for $a" }
        if (-not (Test-Path -LiteralPath $syso) -or (Get-Item -LiteralPath $syso).Length -eq 0) {
            throw "goversioninfo did not produce $syso"
        }

        $exe = Join-Path $outPath "qabas_${Version}_windows_$a.exe"
        $env:GOOS = "windows"; $env:GOARCH = $a; $env:CGO_ENABLED = "0"
        & go build -trimpath -ldflags $ldflags -o $exe .
        if ($LASTEXITCODE -ne 0) { throw "go build failed for $a" }
        Write-Host "built $exe"
    }
} finally {
    Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue
    Pop-Location
}
