# Apply GitHub branch protection rulesets for main and dev.
# Requires: gh CLI authenticated with admin access to qabasone/qabas1-cli
$ErrorActionPreference = "Stop"
$Repo = "qabasone/qabas1-cli"
$RepoRoot = Split-Path -Parent $PSScriptRoot

function Get-RulesetId {
    param([string]$Name)

    $rulesets = gh api "repos/$Repo/rulesets" | ConvertFrom-Json
    $match = $rulesets | Where-Object { $_.name -eq $Name } | Select-Object -First 1
    if ($match) { return $match.id }
    return $null
}

function Apply-Ruleset {
    param(
        [string]$Name,
        [string]$ConfigPath
    )

    $existing = Get-RulesetId -Name $Name
    if ($existing) {
        Write-Host "Updating ruleset '$Name' (id $existing)..."
        Get-Content $ConfigPath -Raw | gh api --method PUT --input=- "repos/$Repo/rulesets/$existing" | Out-Null
    } else {
        Write-Host "Creating ruleset '$Name'..."
        Get-Content $ConfigPath -Raw | gh api --method POST --input=- "repos/$Repo/rulesets" | Out-Null
    }
}

Apply-Ruleset -Name "Protect main" -ConfigPath (Join-Path $RepoRoot ".github/branch-protection-main.json")
Apply-Ruleset -Name "Protect dev" -ConfigPath (Join-Path $RepoRoot ".github/branch-protection-dev.json")

Write-Host "Branch protection applied."
$rulesets = gh api "repos/$Repo/rulesets" | ConvertFrom-Json
$rulesets | ForEach-Object { [PSCustomObject]@{ id = $_.id; name = $_.name; enforcement = $_.enforcement } } | Format-Table -AutoSize
