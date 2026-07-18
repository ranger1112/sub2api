#requires -Version 7.0

<#
.SYNOPSIS
Updates main, merges it into feature/online, validates the merge, and packages
a verified linux/amd64 offline release image.

.EXAMPLE
pwsh -File .\release-online.ps1

.EXAMPLE
pwsh -File .\release-online.ps1 -Plan

.EXAMPLE
pwsh -File .\release-online.ps1 -SkipSync
#>
[CmdletBinding()]
param(
    [string]$MainBranch = 'main',
    [string]$FeatureBranch = 'feature/online',
    [string]$RemoteUrl,
    [string]$GitBash = 'C:\Program Files\Git\bin\bash.exe',
    [switch]$Plan,
    [switch]$SkipSync,
    [switch]$SkipTests
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
if (Test-Path variable:PSNativeCommandUseErrorActionPreference) {
    $PSNativeCommandUseErrorActionPreference = $false
}

$repoRoot = $PSScriptRoot
$originalLocation = Get-Location
$totalTimer = [System.Diagnostics.Stopwatch]::StartNew()

function Write-Step {
    param([Parameter(Mandatory)][string]$Message)

    Write-Host "`n==> $Message" -ForegroundColor Cyan
}

function Invoke-Checked {
    param(
        [Parameter(Mandatory)][string]$Label,
        [Parameter(Mandatory)][scriptblock]$Action
    )

    Write-Step $Label
    & $Action
    $exitCode = $LASTEXITCODE
    if ($exitCode -ne 0) {
        throw "$Label failed with exit code $exitCode."
    }
}

function Invoke-CapturedNative {
    param(
        [Parameter(Mandatory)][string]$FilePath,
        [string[]]$ArgumentList = @()
    )

    $output = & $FilePath @ArgumentList 2>&1
    $exitCode = $LASTEXITCODE
    $text = (($output | ForEach-Object { "$_" }) -join "`n").Trim()
    if ($exitCode -ne 0) {
        throw "$FilePath $($ArgumentList -join ' ') failed with exit code $exitCode.`n$text"
    }
    return $text
}

function Get-GitOutput {
    param([Parameter(Mandatory)][string[]]$ArgumentList)

    return Invoke-CapturedNative -FilePath 'git' -ArgumentList $ArgumentList
}

function Convert-ToHttpsGitUrl {
    param([Parameter(Mandatory)][string]$Url)

    if ($Url -match '^git@([^:]+):(.+)$') {
        return "https://$($Matches[1])/$($Matches[2])"
    }
    if ($Url -match '^ssh://git@([^/]+)/(.+)$') {
        return "https://$($Matches[1])/$($Matches[2])"
    }
    return $Url
}

function Get-Lines {
    param([AllowEmptyString()][string]$Text)

    if ([string]::IsNullOrWhiteSpace($Text)) {
        return @()
    }
    return @($Text -split "`r?`n" | Where-Object { -not [string]::IsNullOrWhiteSpace($_) })
}

function Assert-RequiredTools {
    foreach ($command in @('git', 'docker', 'go', 'pnpm', 'tar')) {
        if (-not (Get-Command $command -ErrorAction SilentlyContinue)) {
            throw "Required command is not available: $command"
        }
    }
    if (-not (Test-Path -LiteralPath $GitBash -PathType Leaf)) {
        throw "Git Bash was not found at: $GitBash"
    }
}

function Assert-NoGitOperationInProgress {
    $gitDir = Get-GitOutput @('rev-parse', '--absolute-git-dir')
    foreach ($marker in @('MERGE_HEAD', 'CHERRY_PICK_HEAD', 'REVERT_HEAD', 'REBASE_HEAD')) {
        if (Test-Path -LiteralPath (Join-Path $gitDir $marker)) {
            throw "Git operation is already in progress: $marker"
        }
    }
}

function Assert-SafeWorktree {
    $trackedStatus = Get-GitOutput @('status', '--porcelain', '--untracked-files=no')
    if (-not [string]::IsNullOrWhiteSpace($trackedStatus)) {
        throw "Tracked files are dirty. Commit or stash them before releasing:`n$trackedStatus"
    }

    $scriptRelative = [System.IO.Path]::GetRelativePath($repoRoot, $PSCommandPath).Replace('\', '/')
    $untracked = Get-Lines (Get-GitOutput @('ls-files', '--others', '--exclude-standard'))
    $unexpected = @($untracked | Where-Object {
        $_ -ne $scriptRelative -and $_ -ne 'images' -and -not $_.StartsWith('images/')
    })
    if ($unexpected.Count -gt 0) {
        throw "Unexpected untracked files would make the release ambiguous:`n$($unexpected -join "`n")"
    }
}

function Get-ChangedPackagePath {
    param([Parameter(Mandatory)][string]$RepositoryPath)

    $relative = $RepositoryPath.Substring('backend/'.Length)
    $directory = Split-Path -Parent $relative
    if ([string]::IsNullOrWhiteSpace($directory)) {
        return '.'
    }
    return './' + $directory.Replace('\', '/')
}

function Invoke-GoPackageBatches {
    param(
        [Parameter(Mandatory)][string]$Label,
        [Parameter(Mandatory)][string[]]$Packages,
        [string[]]$ExtraArguments = @()
    )

    $batchSize = 20
    for ($offset = 0; $offset -lt $Packages.Count; $offset += $batchSize) {
        $last = [Math]::Min($offset + $batchSize - 1, $Packages.Count - 1)
        $batch = @($Packages[$offset..$last])
        Invoke-Checked "$Label ($($offset + 1)-$($last + 1)/$($Packages.Count))" {
            & go test @ExtraArguments @batch
        }
    }
}

function Invoke-ReleaseTests {
    param([Parameter(Mandatory)][string[]]$ChangedFiles)

    $previousCorepackSetting = $env:COREPACK_ENABLE_PROJECT_SPEC
    try {
        Write-Step 'Backend compile preflight'
        Push-Location (Join-Path $repoRoot 'backend')
        try {
            & go test ./cmd/server -run '^$'
            if ($LASTEXITCODE -ne 0) {
                throw "Backend compile preflight failed with exit code $LASTEXITCODE."
            }

            $changedGoFiles = @($ChangedFiles | Where-Object {
                $_.StartsWith('backend/') -and $_.EndsWith('.go')
            })
            $changedPackages = @($changedGoFiles |
                ForEach-Object { Get-ChangedPackagePath $_ } |
                Sort-Object -Unique)
            if ($changedPackages.Count -gt 0) {
                Invoke-GoPackageBatches -Label 'Backend changed-package tests' -Packages $changedPackages
            }

            $unitPackages = @($changedGoFiles | Where-Object {
                $fullPath = Join-Path $repoRoot $_
                (Test-Path -LiteralPath $fullPath) -and
                    (Select-String -LiteralPath $fullPath -Pattern '^//go:build\s+.*\bunit\b' -Quiet)
            } | ForEach-Object { Get-ChangedPackagePath $_ } | Sort-Object -Unique)
            if ($unitPackages.Count -gt 0) {
                Invoke-GoPackageBatches -Label 'Backend unit-tag tests' -Packages $unitPackages -ExtraArguments @('-tags', 'unit')
            }
        }
        finally {
            Pop-Location
        }

        Write-Step 'Frontend dependency check and typecheck'
        Push-Location (Join-Path $repoRoot 'frontend')
        try {
            $env:COREPACK_ENABLE_PROJECT_SPEC = '0'
            $dependencyFilesChanged = $ChangedFiles -contains 'frontend/package.json' -or
                $ChangedFiles -contains 'frontend/pnpm-lock.yaml'
            if ($dependencyFilesChanged -or -not (Test-Path -LiteralPath 'node_modules')) {
                & pnpm install --frozen-lockfile --prefer-offline
                if ($LASTEXITCODE -ne 0) {
                    throw "Frontend dependency install failed with exit code $LASTEXITCODE."
                }
            }

            & pnpm typecheck
            if ($LASTEXITCODE -ne 0) {
                throw "Frontend typecheck failed with exit code $LASTEXITCODE."
            }

            $changedFrontendTests = @($ChangedFiles | Where-Object {
                $_.StartsWith('frontend/') -and ($_ -match '\.(spec|test)\.[cm]?[jt]sx?$')
            } | ForEach-Object { $_.Substring('frontend/'.Length) })
            if ($changedFrontendTests.Count -gt 0) {
                & pnpm test:run -- @changedFrontendTests
                if ($LASTEXITCODE -ne 0) {
                    throw "Frontend changed tests failed with exit code $LASTEXITCODE."
                }
            }
        }
        finally {
            Pop-Location
        }
    }
    finally {
        $env:COREPACK_ENABLE_PROJECT_SPEC = $previousCorepackSetting
    }
}

function Show-ReleasePlan {
    param([Parameter(Mandatory)][string]$FetchUrl)

    $currentBranch = Get-GitOutput @('branch', '--show-current')
    $currentHead = Get-GitOutput @('rev-parse', 'HEAD')
    $localMain = Get-GitOutput @('rev-parse', $MainBranch)
    $remoteLine = Get-GitOutput @('ls-remote', $FetchUrl, "refs/heads/$MainBranch")
    $remoteHead = ($remoteLine -split '\s+')[0]
    $status = Get-GitOutput @('status', '--short', '--branch')
    $dockerPlatform = Invoke-CapturedNative 'docker' @('version', '--format', 'server={{.Server.Version}} platform={{.Server.Os}}/{{.Server.Arch}}')

    Write-Host @"
Release plan
  repository    : $repoRoot
  current branch: $currentBranch
  current HEAD  : $currentHead
  local main    : $localMain
  remote main   : $remoteHead
  fetch URL     : $FetchUrl
  feature branch: $FeatureBranch
  Docker        : $dockerPlatform

Planned stages
  1. Fetch remote main over HTTPS-compatible URL
  2. Fast-forward local main
  3. Merge main into feature/online
  4. Run backend/frontend release checks
  5. Run deploy/package_release.sh with Git Bash
  6. Verify tar, image platform, embedded version, SHA256, and ancestry

Current status
$status
"@
}

try {
    Set-Location $repoRoot
    Assert-RequiredTools
    [void](Get-GitOutput @('rev-parse', '--show-toplevel'))

    if ([string]::IsNullOrWhiteSpace($RemoteUrl)) {
        $RemoteUrl = Convert-ToHttpsGitUrl (Get-GitOutput @('remote', 'get-url', 'origin'))
    }

    if ($Plan) {
        Show-ReleasePlan -FetchUrl $RemoteUrl
        return
    }

    Assert-NoGitOperationInProgress
    Assert-SafeWorktree

    $currentBranch = Get-GitOutput @('branch', '--show-current')
    if ($currentBranch -ne $FeatureBranch) {
        throw "Run this script from $FeatureBranch; current branch is $currentBranch."
    }

    $featureBeforeMerge = Get-GitOutput @('rev-parse', 'HEAD')
    if (-not $SkipSync) {
        $remoteTrackingRef = "refs/remotes/origin/$MainBranch"
        $refspec = "${MainBranch}:$remoteTrackingRef"
        Invoke-Checked "Fetch $MainBranch from $RemoteUrl" {
            & git fetch $RemoteUrl $refspec
        }

        Invoke-Checked "Checkout $MainBranch" {
            & git checkout $MainBranch
        }
        try {
            Invoke-Checked "Fast-forward local $MainBranch" {
                & git merge --ff-only $remoteTrackingRef
            }
        }
        finally {
            $branchAfterMainUpdate = Get-GitOutput @('branch', '--show-current')
            if ($branchAfterMainUpdate -eq $MainBranch) {
                Invoke-Checked "Return to $FeatureBranch" {
                    & git checkout $FeatureBranch
                }
            }
        }

        Invoke-Checked "Merge $MainBranch into $FeatureBranch" {
            & git merge $MainBranch
        }
        Assert-SafeWorktree
    }

    $head = Get-GitOutput @('rev-parse', 'HEAD')
    $shortCommit = Get-GitOutput @('rev-parse', '--short=8', 'HEAD')
    $changedRange = if ($featureBeforeMerge -eq $head) { 'HEAD^..HEAD' } else { "$featureBeforeMerge..HEAD" }
    $changedFiles = @(Get-Lines (Get-GitOutput @('diff', '--name-only', $changedRange)))

    if (-not $SkipTests) {
        Invoke-ReleaseTests -ChangedFiles $changedFiles
    }
    else {
        Write-Warning 'Release tests were skipped explicitly.'
    }

    Assert-SafeWorktree
    $dockerPlatform = Invoke-CapturedNative 'docker' @('version', '--format', '{{.Server.Os}}/{{.Server.Arch}}')
    if ($dockerPlatform -ne 'linux/amd64') {
        throw "Docker server platform must be linux/amd64, got: $dockerPlatform"
    }

    $packageTimer = [System.Diagnostics.Stopwatch]::StartNew()
    try {
        Invoke-Checked 'Build and package release image' {
            & $GitBash 'deploy/package_release.sh'
        }
    }
    finally {
        $packageTimer.Stop()
    }

    $branchTag = $FeatureBranch.Replace('/', '-')
    $imageTag = "sub2api:${branchTag}-${shortCommit}"
    $tarPath = Join-Path $repoRoot "deploy/sub2api-${branchTag}-${shortCommit}-linux-amd64.tar"
    $version = (Get-Content -LiteralPath (Join-Path $repoRoot 'backend/cmd/server/VERSION') -Raw).Trim()
    if (-not (Test-Path -LiteralPath $tarPath -PathType Leaf)) {
        throw "Expected release tar was not created: $tarPath"
    }

    Invoke-Checked 'Load packaged image' {
        & docker load -i $tarPath
    }
    $imagePlatform = Invoke-CapturedNative 'docker' @('image', 'inspect', $imageTag, '--format', '{{.Os}}/{{.Architecture}}')
    if ($imagePlatform -ne 'linux/amd64') {
        throw "Packaged image platform mismatch: $imagePlatform"
    }

    $versionOutput = Invoke-CapturedNative 'docker' @('run', '--rm', '--entrypoint', '/app/sub2api', $imageTag, '--version')
    if ($versionOutput -notmatch [regex]::Escape("Sub2API $version")) {
        throw "Embedded version mismatch. Expected Sub2API $version, got:`n$versionOutput"
    }

    $tarEntries = Get-Lines (Invoke-CapturedNative 'tar' @('tf', $tarPath))
    if ($tarEntries -notcontains 'index.json' -or $tarEntries -notcontains 'manifest.json') {
        throw 'Release tar must contain both index.json and manifest.json.'
    }

    $hash = Get-FileHash -LiteralPath $tarPath -Algorithm SHA256
    $tarFile = Get-Item -LiteralPath $tarPath
    $originMain = Get-GitOutput @('rev-parse', "refs/remotes/origin/$MainBranch")
    $remoteLine = Get-GitOutput @('ls-remote', $RemoteUrl, "refs/heads/$MainBranch")
    $remoteMain = ($remoteLine -split '\s+')[0]
    if ($remoteMain -ne $originMain) {
        throw "Remote $MainBranch advanced during the release. Built $originMain, now $remoteMain."
    }

    & git merge-base --is-ancestor $originMain HEAD
    if ($LASTEXITCODE -ne 0) {
        throw "Current HEAD does not contain origin/$MainBranch."
    }
    Assert-SafeWorktree

    $totalTimer.Stop()
    Write-Host @"

Release verified
  merge HEAD          : $head
  origin/$MainBranch         : $originMain
  image               : $imageTag
  platform            : $imagePlatform
  version             : Sub2API $version
  tar                 : $tarPath
  tar bytes           : $($tarFile.Length)
  SHA256              : $($hash.Hash.ToLowerInvariant())
  Docker-only seconds : $([Math]::Round($packageTimer.Elapsed.TotalSeconds, 1))
  end-to-end seconds  : $([Math]::Round($totalTimer.Elapsed.TotalSeconds, 1))

No Git push, image push, or deployment was performed.
"@ -ForegroundColor Green
}
catch {
    Write-Error $_
    exit 1
}
finally {
    if ($totalTimer.IsRunning) {
        $totalTimer.Stop()
    }
    Set-Location $originalLocation
}
