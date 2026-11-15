# Bootstrap the qabas CLI on Windows.
#   iwr -useb https://install.qabas.one/install.ps1 -OutFile install.ps1; .\install.ps1
#   iwr -useb https://install.qabas.one/install.ps1 -OutFile install.ps1; .\install.ps1 -CliOnly
#
# Pin CLI version:
#   $env:VERSION = "1.0.0"; .\install.ps1 -CliOnly
#
# Private GitHub repos:
#   $env:GITHUB_TOKEN = gh auth token
#   iwr -useb https://install.qabas.one/install.ps1 -Headers @{ Authorization = "Bearer $env:GITHUB_TOKEN" } -OutFile install.ps1
#   .\install.ps1 -CliOnly
#Requires -Version 5.1
[CmdletBinding()]
param(
    [switch]$Help,
    [switch]$CliOnly,
    [switch]$BrowserAuth,
    [switch]$Force,
    [switch]$AllowDowngrade,
    [switch]$SkipSignatureCheck,
    [string]$Release,
    [string]$TokenFile,
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]]$RemainingArgs
)

$ErrorActionPreference = "Stop"

$Repo = if ($env:REPO) { $env:REPO } else { "qabas1/qabas1-cli" }
$Version = if ($env:VERSION) { $env:VERSION } else { "latest" }
$InstallDir = if ($env:INSTALL_DIR) { $env:INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA "Programs\qabas" }
$GithubApi = if ($env:GITHUB_API) { $env:GITHUB_API } else { "https://api.github.com" }
$GithubReleases = if ($env:GITHUB_RELEASES) { $env:GITHUB_RELEASES } else { "https://github.com/$Repo/releases" }
$GithubToken = if ($env:GITHUB_TOKEN) { $env:GITHUB_TOKEN } elseif ($env:GH_TOKEN) { $env:GH_TOKEN } else { "" }
$ForceInstall = if ($Force -or ($env:FORCE -eq "1")) { $true } else { $false }
$AllowDowngradeInstall = if ($AllowDowngrade -or ($env:ALLOW_DOWNGRADE -eq "1")) { $true } else { $false }
$SkipSignature = if ($SkipSignatureCheck -or ($env:SKIP_SIGNATURE_CHECK -eq "1")) { $true } else { $false }
# Authenticode signer subject for release assets (SignPath OSS certificate, or a QabasOne-owned one).
$ExpectedSigner = if ($env:QABAS_SIGNER_PATTERN) { $env:QABAS_SIGNER_PATTERN } else { '^CN=(SignPath Foundation|QabasOne)(,|$)' }
$InstallLog = Join-Path $env:TEMP "qabas-install.log"

function Log([string]$Message) { Write-Host "[qabas-install] $Message" }
function Die([string]$Message) { Log "ERROR: $Message"; exit 1 }
function Die-Usage([string]$Message) { Show-Usage; Log "ERROR: $Message"; exit 1 }

function Show-Usage {
        @"
Usage: install.ps1 [OPTIONS]

Install or update the qabas CLI on Windows.

Options:
    -Help                 Show this help and exit
    -CliOnly              Install or update the CLI only (default on Windows)
    -BrowserAuth          Open GitHub browser login through 'gh auth login --web'
    -Force                Reinstall the selected CLI version
    -AllowDowngrade       Allow installing an older CLI version
    -SkipSignatureCheck   Do not require a valid Authenticode signature (not recommended)
    -Release VERSION      Product release (not supported on Windows)
    -TokenFile PATH       Read a GitHub token from a file

Environment:
    VERSION, REPO, INSTALL_DIR, GITHUB_API
    GITHUB_TOKEN, GH_TOKEN, QABAS_GITHUB_TOKEN_FILE
    SKIP_SIGNATURE_CHECK=1, QABAS_SIGNER_PATTERN (regex for the signer subject)

The MSI install log is written to %TEMP%\qabas-install.log.

Examples:
    iwr -useb https://install.qabas.one/install.ps1 -OutFile install.ps1
    .\install.ps1 -CliOnly
    .\install.ps1 -BrowserAuth -CliOnly
"@ | Write-Host
}

if ($Help) { Show-Usage; exit 0 }
if ($RemainingArgs -and $RemainingArgs.Count -gt 0) {
    Die-Usage "unknown argument(s): $($RemainingArgs -join ' ')"
}

function Get-TrimmedFileContent([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return "" }
    try {
        return (Get-Content -LiteralPath $Path -Raw -ErrorAction Stop).Trim()
    } catch {
        return ""
    }
}

function Load-GithubTokenFromDisk {
    if ($script:GithubToken) { return }

    if ($env:QABAS_GITHUB_TOKEN_FILE) {
        $token = Get-TrimmedFileContent $env:QABAS_GITHUB_TOKEN_FILE
        if (-not $token) { Die "QABAS_GITHUB_TOKEN_FILE is not readable or is empty: $env:QABAS_GITHUB_TOKEN_FILE" }
        $script:GithubToken = $token
        Log "using GitHub token from QABAS_GITHUB_TOKEN_FILE"
        return
    }

    $userTokenFile = Join-Path $env:USERPROFILE ".config\qabas\github-token"
    $token = Get-TrimmedFileContent $userTokenFile
    if ($token) {
        $script:GithubToken = $token
        Log "using GitHub token from $userTokenFile"
        return
    }

    $gh = Get-Command gh -ErrorAction SilentlyContinue
    if ($gh) {
        $token = (& $gh.Source auth token 2>$null | Out-String).Trim()
        if ($token) {
            $script:GithubToken = $token
            Log "using token from gh auth"
        }
    }
}

function Die-AuthRequired([string]$Reason) {
    Log $Reason
    if (-not $GithubToken) {
        Write-Host @"

The qabasone GitHub repositories are private — GitHub authentication is required.

Securely authenticate using your browser (recommended):

  gh auth login --web --git-protocol https --hostname github.com
  `$env:GITHUB_TOKEN = gh auth token
  .\install.ps1 -CliOnly

Or use -BrowserAuth to automate browser login:

  .\install.ps1 -BrowserAuth -CliOnly

Or pass a token file:

  .\install.ps1 -TokenFile C:\Users\you\.config\qabas\github-token -CliOnly

IMPORTANT: Never pass tokens on the command line (visible in process lists).
"@
    } else {
        Write-Host @"

GITHUB_TOKEN is set but GitHub still refused the request.
Verify the token has:
  - 'repo' scope for private repository access
  - 'read:packages' scope for container registry access
If using SAML SSO, authorize the org first at: https://github.com/settings/tokens
"@
    }
    exit 1
}

if ($TokenFile) {
    if (-not (Test-Path -LiteralPath $TokenFile -PathType Leaf)) {
        Die "token file not readable: $TokenFile (check file path and permissions)"
    }
    $GithubToken = (Get-Content -LiteralPath $TokenFile -Raw).Trim()
    if (-not $GithubToken) { Die "token file is empty or unreadable: $TokenFile" }
    Log "loaded GitHub token from $TokenFile"
}

function Invoke-BrowserAuth {
    $gh = Get-Command gh -ErrorAction SilentlyContinue
    if (-not $gh) { Die "-BrowserAuth requires the GitHub CLI (https://cli.github.com/)" }
    
    Log "launching GitHub browser authentication..."
    & $gh.Source auth login --web --git-protocol https --hostname github.com 2>$null
    if ($LASTEXITCODE -ne 0) { Die "GitHub browser authentication failed (check your browser)" }
    
    $script:GithubToken = (& $gh.Source auth token 2>$null | Out-String).Trim()
    if (-not $script:GithubToken) { Die "GitHub browser login completed but no token was returned (check permissions)" }
    Log "successfully authenticated with GitHub"
}

Load-GithubTokenFromDisk
if ($BrowserAuth) { Invoke-BrowserAuth }

if ($env:OS -ne "Windows_NT") {
    Die "unsupported OS (Windows only; use install.sh on Linux)"
}

switch ($env:PROCESSOR_ARCHITECTURE) {
    "AMD64" { $GoArch = "amd64" }
    "ARM64" { $GoArch = "arm64" }
    default { Die "unsupported architecture: $($env:PROCESSOR_ARCHITECTURE) (supported: amd64, arm64)" }
}

function Normalize-Tag([string]$Value) {
    # Branch is "dev"; rolling release tag is "dev-latest" (avoids branch/tag name clash).
    if ($Value -eq "dev") { return "dev-latest" }
    if ($Value.StartsWith("v")) { return $Value }
    if ($Value.StartsWith("dev")) { return $Value }
    return "v$Value"
}

function Normalize-SemVer([string]$Value) {
    if ($Value.StartsWith("v")) { return $Value.Substring(1) }
    return $Value
}

function Compare-SemVer([string]$A, [string]$B) {
    $a = Normalize-SemVer $A
    $b = Normalize-SemVer $B
    if ($a -eq $b) { return 0 }
    try {
        $va = [version]$a
        $vb = [version]$b
        if ($va -lt $vb) { return -1 }
        if ($va -gt $vb) { return 1 }
        return 0
    } catch {
        if ($a -lt $b) { return -1 }
        if ($a -gt $b) { return 1 }
        return 0
    }
}

function Get-QabasBin {
    $pf = Join-Path ${env:ProgramFiles} "qabas\qabas.exe"
    if (Test-Path -LiteralPath $pf) { return $pf }
    $local = Join-Path $InstallDir "qabas.exe"
    if (Test-Path -LiteralPath $local) { return $local }
    $cmd = Get-Command qabas -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    return $null
}

function Get-InstalledCliVersion {
    $bin = Get-QabasBin
    if (-not $bin) { return $null }
    $out = & $bin version 2>$null
    foreach ($line in $out) {
        if ($line -match "^qabas CLI\s+(\S+)") { return $Matches[1] }
    }
    return $null
}

function Invoke-GithubApi([string]$Url) {
    $headers = @{
        Accept = "application/vnd.github+json"
        "X-GitHub-Api-Version" = "2022-11-28"
    }
    if ($GithubToken) { $headers.Authorization = "Bearer $GithubToken" }
    try {
        return Invoke-RestMethod -Uri $Url -Headers $headers -Method Get
    } catch {
        return $null
    }
}

function Get-ReleaseEndpoint {
    if ($Version -eq "latest") {
        return "$GithubApi/repos/$Repo/releases/latest"
    }
    $tag = Normalize-Tag $Version
    return "$GithubApi/repos/$Repo/releases/tags/$tag"
}

function Resolve-Tag {
    if ($Version -ne "latest") {
        return Normalize-Tag $Version
    }
    $json = Invoke-GithubApi (Get-ReleaseEndpoint)
    if (-not $json -or -not $json.tag_name) {
        Die-AuthRequired "Could not resolve the latest CLI release on $Repo."
    }
    return [string]$json.tag_name
}

function Get-AssetId([object]$ReleaseJson, [string]$AssetName) {
    foreach ($asset in $ReleaseJson.assets) {
        if ($asset.name -eq $AssetName) { return [int]$asset.id }
    }
    return 0
}

function Download-ReleaseAsset([int]$AssetId, [string]$Dest) {
    if (-not $GithubToken) { Die-AuthRequired "Release download requires GITHUB_TOKEN or GH_TOKEN." }
    $headers = @{
        Authorization = "Bearer $GithubToken"
        Accept = "application/octet-stream"
        "X-GitHub-Api-Version" = "2022-11-28"
    }
    $url = "$GithubApi/repos/$Repo/releases/assets/$AssetId"
    try {
        Invoke-WebRequest -Uri $url -Headers $headers -OutFile $Dest -UseBasicParsing
    } catch {
        Die-AuthRequired "Failed to download release asset $AssetId from $Repo."
    }
}

function Download-Public([string]$Url, [string]$Dest) {
    try {
        Invoke-WebRequest -Uri $Url -OutFile $Dest -UseBasicParsing
    } catch {
        return $false
    }
    return $true
}

function Verify-Sha256([string]$SumsFile, [string]$FileName) {
    $escaped = [regex]::Escape($FileName)
    $line = Select-String -Path $SumsFile -Pattern "^\s*([0-9a-fA-F]{64})\s+$escaped\s*$" | Select-Object -First 1
    if (-not $line) { return $false }
    $expected = $line.Matches[0].Groups[1].Value.ToLower()
    $filePath = Join-Path (Split-Path $SumsFile -Parent) $FileName
    $actual = (Get-FileHash -LiteralPath $filePath -Algorithm SHA256).Hash.ToLower()
    return $expected -eq $actual
}

function Verify-Signature([string]$Path, [string]$Tag) {
    $name = Split-Path $Path -Leaf
    if ($SkipSignature) {
        Log "WARNING: skipping Authenticode signature check for $name (-SkipSignatureCheck)"
        return
    }
    if ($Tag -eq "dev-latest") {
        Log "dev channel builds are not code-signed; skipping Authenticode signature check"
        return
    }
    $sig = Get-AuthenticodeSignature -LiteralPath $Path
    switch ($sig.Status) {
        "Valid" { }
        "NotSigned" { Die "$name is not code-signed; refusing to install (use -SkipSignatureCheck to override)" }
        default { Die "$name has an invalid Authenticode signature ($($sig.Status)): $($sig.StatusMessage)" }
    }
    $subject = [string]$sig.SignerCertificate.Subject
    if ($subject -notmatch $ExpectedSigner) {
        Die "$name is signed by an unexpected publisher: $subject"
    }
    Log "verified Authenticode signature ($subject)"
}

function Add-ToUserPath([string]$Dir) {
    $current = [Environment]::GetEnvironmentVariable("Path", "User")
    $parts = @()
    if ($current) { $parts = $current -split ";" | Where-Object { $_ } }
    foreach ($p in $parts) {
        if ($p.TrimEnd("\") -ieq $Dir.TrimEnd("\")) { return }
    }
    $updated = if ($current) { "$current;$Dir" } else { $Dir }
    [Environment]::SetEnvironmentVariable("Path", $updated, "User")
    if ($env:Path -notlike "*$Dir*") { $env:Path = "$env:Path;$Dir" }
    Log "added $Dir to user PATH (open a new terminal if qabas is not found)"
}

function Plan-CliInstall([string]$Installed, [string]$Target) {
    if (-not $Installed) { return $true }
    $cmp = Compare-SemVer $Installed $Target
    if ($cmp -eq 0 -and -not $ForceInstall) {
        Log "qabas CLI $Installed already installed (target $Target); skipping download"
        return $false
    }
    if ($cmp -eq 1 -and -not $ForceInstall -and -not $AllowDowngradeInstall) {
        Die "refusing CLI downgrade $Installed -> $Target (use -AllowDowngrade or set ALLOW_DOWNGRADE=1)"
    }
    if ($cmp -eq -1) { Log "upgrading qabas CLI $Installed -> $Target" }
    elseif ($cmp -eq 1) { Log "downgrading qabas CLI $Installed -> $Target" }
    return $true
}

function Install-Cli([string]$Tag, [string]$SemVer, [string]$Asset, [string]$AssetUrl, [string]$SumsUrl) {
    $tmpdir = New-TemporaryFile | ForEach-Object { Remove-Item $_; New-Item -ItemType Directory -Path $_.FullName }
    try {
        if ($GithubToken) {
            Log "using authenticated GitHub API (private repo)"
            $releaseJson = Invoke-GithubApi (Get-ReleaseEndpoint)
            if (-not $releaseJson -or -not $releaseJson.tag_name) {
                Die-AuthRequired "Could not read release $Tag metadata from $Repo."
            }
            $assetId = Get-AssetId $releaseJson $Asset
            if (-not $assetId -and $Asset.EndsWith(".msi")) {
                Log "release $Tag has no $Asset; falling back to raw exe"
                $Asset = "qabas_${SemVer}_windows_${GoArch}.exe"
                $AssetUrl = "$GithubReleases/download/$Tag/$Asset"
                $assetId = Get-AssetId $releaseJson $Asset
            }
            if (-not $assetId) { Die "release $Tag does not publish $Asset" }
            $sumsId = Get-AssetId $releaseJson "SHA256SUMS"
            if (-not $sumsId) { Die "release $Tag does not publish SHA256SUMS" }

            Log "downloading $Asset (release asset $assetId)"
            Download-ReleaseAsset $assetId (Join-Path $tmpdir.FullName $Asset)
            Log "downloading SHA256SUMS (release asset $sumsId)"
            Download-ReleaseAsset $sumsId (Join-Path $tmpdir.FullName "SHA256SUMS")
        } else {
            Log "downloading $AssetUrl"
            if (-not (Download-Public $AssetUrl (Join-Path $tmpdir.FullName $Asset))) {
                if ($Asset.EndsWith(".msi")) {
                    Log "could not download $Asset; falling back to raw exe"
                    $Asset = "qabas_${SemVer}_windows_${GoArch}.exe"
                    $AssetUrl = "$GithubReleases/download/$Tag/$Asset"
                    Log "downloading $AssetUrl"
                    if (-not (Download-Public $AssetUrl (Join-Path $tmpdir.FullName $Asset))) {
                        Die-AuthRequired "Could not download $Asset (release $Tag)."
                    }
                } else {
                    Die-AuthRequired "Could not download $Asset (release $Tag)."
                }
            }
            Log "downloading $SumsUrl"
            if (-not (Download-Public $SumsUrl (Join-Path $tmpdir.FullName "SHA256SUMS"))) {
                Die-AuthRequired "Could not download SHA256SUMS for release $Tag."
            }
        }

        if (-not (Verify-Sha256 (Join-Path $tmpdir.FullName "SHA256SUMS") $Asset)) {
            Die "checksum verification failed"
        }

        $src = Join-Path $tmpdir.FullName $Asset
        Verify-Signature $src $Tag
        Unblock-File -LiteralPath $src -ErrorAction SilentlyContinue

        if ($Asset.EndsWith(".msi")) {
            Log "installing $Asset via msiexec (log: $InstallLog)"
            $p = Start-Process -FilePath "msiexec.exe" -ArgumentList @("/i", "`"$src`"", "/qn", "/norestart", "/l*v", "`"$InstallLog`"") -Wait -PassThru
            if ($p.ExitCode -eq 1925 -or $p.ExitCode -eq 1603) {
                Die "msiexec failed with exit code $($p.ExitCode); if PowerShell is not elevated, retry as Administrator. Details: $InstallLog"
            }
            if ($p.ExitCode -ne 0 -and $p.ExitCode -ne 3010) {
                Die "msiexec failed with exit code $($p.ExitCode). Details: $InstallLog"
            }
            Log "installed qabas via MSI ($Tag)"
        } else {
            New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
            $dest = Join-Path $InstallDir "qabas.exe"
            Copy-Item -LiteralPath $src -Destination $dest -Force
            Log "installed $dest ($Tag)"
            Add-ToUserPath $InstallDir
        }
    } finally {
        Remove-Item -LiteralPath $tmpdir.FullName -Recurse -Force -ErrorAction SilentlyContinue
    }
}

if (-not $CliOnly) {
    if ($Release) {
        Die "install.ps1 installs the qabas CLI only. Afterwards run 'qabas install $Release' from an elevated PowerShell with Docker Desktop (Linux containers)."
    }
    Log "Windows bootstrap installs the qabas CLI only; then run 'qabas install' from an elevated PowerShell with Docker Desktop."
    $CliOnly = $true
}

$Tag = Resolve-Tag
# Asset basename uses channel id "dev"; GitHub tag is "dev-latest".
if ($Tag -eq "dev-latest" -or $Version -eq "dev") {
    $SemVer = "dev"
} elseif ($Tag.StartsWith("v")) {
    $SemVer = $Tag.Substring(1)
} else {
    $SemVer = $Tag
}
$Asset = "qabas_${SemVer}_windows_${GoArch}.msi"
$AssetUrl = "$GithubReleases/download/$Tag/$Asset"
$SumsUrl = "$GithubReleases/download/$Tag/SHA256SUMS"

$installed = Get-InstalledCliVersion
$shouldDownload = Plan-CliInstall $installed $SemVer
if ($shouldDownload) {
    Install-Cli $Tag $SemVer $Asset $AssetUrl $SumsUrl
}

$bin = Get-QabasBin
if ($bin) { & $bin version }

Log "CLI-only install complete"
