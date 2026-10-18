# Windows: put the binary at bin\rein.exe (what hooks/hooks.json runs; Windows resolves "bin/rein" to rein.exe).
# Install order mirrors scripts/install.sh:
#   1. build from source            when this is a full checkout (go.mod + cmd/rein) and Go is available
#   2. copy the local artifact      dist\rein-windows-<arch>.exe (scripts/build.sh output)
#   3. verified release download    rein-windows-<arch>.exe + SHA256SUMS from the GitHub release for this
#                                   package version; REIN_RELEASE_URL and REIN_RELEASE_REPO override the location
#   4. fail nonzero with a clear message
# UNVERIFIED on a real Windows machine - see README "Status".
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..")
New-Item -ItemType Directory -Force -Path bin | Out-Null

$arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
$asset = "rein-windows-$arch.exe"
$version = $null
if (Test-Path "package.json") {
  $version = (Get-Content "package.json" -Raw | ConvertFrom-Json).version
}
if (-not $version -and (Test-Path ".claude-plugin\plugin.json")) {
  $version = (Get-Content ".claude-plugin\plugin.json" -Raw | ConvertFrom-Json).version
}
$repo = if ($env:REIN_RELEASE_REPO) { $env:REIN_RELEASE_REPO } else { "voravitl/rein" }
$url = if ($env:REIN_RELEASE_URL) {
  $env:REIN_RELEASE_URL.TrimEnd("/")
} elseif ($version) {
  "https://github.com/$repo/releases/download/v$version"
} else { $null }

$bin = Join-Path (Get-Location) "bin\rein.exe"
$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("rein-" + [guid]::NewGuid().ToString("N") + ".exe")
$tmpSums = "$tmp.sums"
$how = $null
$buildError = $null

function Get-ReleaseFile([string]$uri, [string]$dest) {
  if ($uri.StartsWith("file://")) {
    Copy-Item -LiteralPath ([uri]$uri).LocalPath -Destination $dest -Force
  } else {
    Invoke-WebRequest -UseBasicParsing -Uri $uri -OutFile $dest -TimeoutSec 120
  }
}

# A stale binary must not mask a failed build/download.
Remove-Item -Force -ErrorAction SilentlyContinue $bin
try {
  # 1. full checkout with Go
  if ((Test-Path "go.mod") -and (Test-Path "cmd\rein") -and (Get-Command go -ErrorAction SilentlyContinue)) {
    $env:CGO_ENABLED = "0"
    try {
      go build -trimpath -ldflags "-s -w" -o $tmp .\cmd\rein
      if ($LASTEXITCODE -ne 0) { throw "go build exited $LASTEXITCODE" }
      $how = "built from source"
    } catch {
      $buildError = $_.Exception.Message
      Remove-Item -Force -ErrorAction SilentlyContinue $tmp
      Write-Warning "rein: go build failed ($buildError); trying the prebuilt options"
    }
  }

  # 2. a local dist artifact
  if (-not $how -and (Test-Path "dist\$asset")) {
    Copy-Item -LiteralPath "dist\$asset" -Destination $tmp -Force
    $how = "copied dist\$asset"
  }

  # 3. verified release download
  if (-not $how -and $url) {
    try {
      Get-ReleaseFile "$url/$asset" $tmp
      Get-ReleaseFile "$url/SHA256SUMS" $tmpSums
      $want = $null
      foreach ($line in Get-Content $tmpSums) {
        if ($line -match '^([0-9a-fA-F]{64})\s+\*?(.+)$') {
          if ($matches[2].Trim() -eq $asset) {
            $want = $matches[1].ToLowerInvariant()
            break
          }
        }
      }
      if (-not $want) { throw "SHA256SUMS has no entry for $asset" }
      $got = (Get-FileHash -Algorithm SHA256 -LiteralPath $tmp).Hash.ToLowerInvariant()
      if ($want -ne $got) { throw "checksum mismatch for $asset" }
      $how = "downloaded release v$version"
    } catch {
      Remove-Item -Force -ErrorAction SilentlyContinue $tmp, $tmpSums
      throw "rein: could not download and verify $asset from $url ($($_.Exception.Message))"
    }
  }

  if (-not $how) {
    $tried = "go build (a full checkout and Go 1.26+), dist\$asset, and the release download"
    if ($url) { $tried += " at $url" }
    if ($buildError) { $tried += "; last build error: $buildError" }
    throw "rein: cannot provide a native binary for windows/$arch (tried $tried). Set REIN_RELEASE_URL to a directory holding $asset + SHA256SUMS."
  }

  # Validate before publishing: a wrong-platform or broken file must never replace the hook path.
  & $tmp version
  if ($LASTEXITCODE -ne 0) {
    Remove-Item -Force -ErrorAction SilentlyContinue $tmp
    throw "rein: installed candidate does not run on this machine ($how); removed it"
  }
  Move-Item -Force -LiteralPath $tmp -Destination $bin
  Write-Host "rein: installed bin\rein.exe ($how)"
} finally {
  Remove-Item -Force -ErrorAction SilentlyContinue $tmp, $tmpSums
}
