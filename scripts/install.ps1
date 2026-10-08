# Windows: put the binary at bin\rein.exe (what hooks/hooks.json runs; Windows resolves "bin/rein" to rein.exe).
# UNVERIFIED on a real Windows machine - see README "Status".
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..")
New-Item -ItemType Directory -Force -Path bin | Out-Null
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
Remove-Item -Force -ErrorAction SilentlyContinue bin\rein.exe   # a stale binary must not mask a failed build
if (Get-Command go -ErrorAction SilentlyContinue) {
  $env:CGO_ENABLED = "0"; go build -trimpath -ldflags "-s -w" -o bin\rein.exe .\cmd\rein
  if ($LASTEXITCODE -ne 0) { throw "go build failed ($LASTEXITCODE)" }
} elseif (Test-Path "dist\rein-windows-$arch.exe") {
  Copy-Item "dist\rein-windows-$arch.exe" bin\rein.exe
} else { throw "no Go toolchain and no dist\rein-windows-$arch.exe" }
& bin\rein.exe version
