param(
    [switch]$PurgeState
)
$ErrorActionPreference = "Stop"
$Dest = if ($env:NIBIA_BIN_DIR) { $env:NIBIA_BIN_DIR } else { Join-Path $HOME ".nibia\bin" }

foreach ($name in @("nibia.exe", "nibia-agent.exe", "nibia-controller.exe")) {
    $path = Join-Path $Dest $name
    if (Test-Path $path) { Remove-Item $path -Force }
}

$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath) {
    $parts = $userPath -split ';' | Where-Object { $_ -and $_ -ne $Dest }
    [Environment]::SetEnvironmentVariable("Path", ($parts -join ';'), "User")
}
Write-Host "Removed NIBIA binaries from $Dest"

if ($PurgeState) {
    $state = Join-Path $HOME ".nibia"
    if (Test-Path $state) { Remove-Item $state -Recurse -Force }
    Write-Host "Removed NIBIA state/runtime from $state"
} else {
    Write-Host "Preserved $HOME\.nibia (pairings, state, managed runtime)."
}
