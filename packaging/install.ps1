param(
    [switch]$SkipSetup
)
$ErrorActionPreference = "Stop"
$Here = Split-Path -Parent $MyInvocation.MyCommand.Path
$Dest = if ($env:NIBIA_BIN_DIR) { $env:NIBIA_BIN_DIR } else { Join-Path $HOME ".nibia\bin" }

$arch = if ($env:PROCESSOR_ARCHITEW6432) {
    $env:PROCESSOR_ARCHITEW6432
} else {
    $env:PROCESSOR_ARCHITECTURE
}
if ([string]::IsNullOrWhiteSpace($arch)) {
    throw "Unable to detect Windows architecture."
}
$arch = $arch.ToUpperInvariant()
if ($arch -ne "AMD64") {
    throw "This package is for Windows amd64/x64; detected $arch."
}

New-Item -ItemType Directory -Force -Path $Dest | Out-Null
Copy-Item (Join-Path $Here "nibia.exe") (Join-Path $Dest "nibia.exe") -Force
Copy-Item (Join-Path $Here "nibia-agent.exe") (Join-Path $Dest "nibia-agent.exe") -Force
Copy-Item (Join-Path $Here "nibia-controller.exe") (Join-Path $Dest "nibia-controller.exe") -Force

$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
$parts = @()
if ($userPath) { $parts = $userPath -split ';' | Where-Object { $_ } }
if ($parts -notcontains $Dest) {
    $newPath = if ($userPath) { "$Dest;$userPath" } else { $Dest }
    [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
    Write-Host "User PATH updated."
}
if (($env:Path -split ';') -notcontains $Dest) {
    $env:Path = "$Dest;$env:Path"
}

Write-Host "NIBIA installed to $Dest"
if (-not $SkipSetup) {
    & (Join-Path $Dest "nibia.exe") setup
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
    & (Join-Path $Dest "nibia.exe") doctor --local
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
}
Write-Host ""
Write-Host "Ready. Run: nibia version"
