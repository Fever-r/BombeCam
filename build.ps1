# Builds BombeCam from source into bin\<target>\ (and .\bombecam.exe on Windows).
# Needs Go (https://go.dev/dl/). Release downloads are made with: go run ./tools/release
# The Osaio server key and app ID are built in (obscured) from osaio-setup.txt or
# BOMBECAM_SERVER_KEY / BOMBECAM_APP_ID; go run ./tools/setup saves the file.
param(
    [ValidateSet('windows-amd64', 'linux-amd64')]
    [string]$Target = 'windows-amd64'
)
$ErrorActionPreference = 'Stop'
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    Write-Host 'Go is not installed. Install it with:  winget install GoLang.Go' -ForegroundColor Yellow
    Write-Host '(or from https://go.dev/dl/), then open a new window and run this again.'
    exit 1
}
$previousOS = $env:GOOS
$previousArch = $env:GOARCH
$previousCGO = $env:CGO_ENABLED
Push-Location $PSScriptRoot
try {
    # The setup tool runs on this PC, so build it for this PC whatever GOOS and
    # GOARCH say (the finally block restores them). Its note on stderr when no
    # key is set up must not stop the script (it would in PowerShell ISE).
    $env:GOOS = & go env GOHOSTOS
    $env:GOARCH = & go env GOHOSTARCH
    $savedPreference = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try { $keyFlag = & go run ./tools/setup -ldflags } finally { $ErrorActionPreference = $savedPreference }
    if ($LASTEXITCODE -ne 0) { throw 'Could not get the Osaio server key or app ID for the build (see above). Fix osaio-setup.txt with: go run ./tools/setup, or fix or clear BOMBECAM_SERVER_KEY / BOMBECAM_APP_ID.' }

    $parts = $Target.Split('-')
    $env:GOOS = $parts[0]
    $env:GOARCH = $parts[1]
    $env:CGO_ENABLED = '0'
    $destination = Join-Path $PSScriptRoot "bin/$Target"
    $null = New-Item -ItemType Directory -Path $destination -Force
    $extension = if ($env:GOOS -eq 'windows') { '.exe' } else { '' }

    # Build primary single user-facing executable (bombecam.exe on windows, bombecam on linux).
    # On Windows it is a window-less (GUI) program with a tray icon;
    # bombecam-gateway.exe below is the same gateway with a console window.
    $bombecamFile = Join-Path $destination ("bombecam" + $extension)
    $ldflags = if ($env:GOOS -eq 'windows') { "-ldflags=-s -w -H=windowsgui $keyFlag" } else { "-ldflags=-s -w $keyFlag" }
    & go build -buildvcs=false -trimpath $ldflags -o $bombecamFile "./cmd/bombecam-gateway"
    if ($LASTEXITCODE -ne 0) { throw "Build failed: bombecam ($Target)" }

    if ($env:GOOS -eq 'windows') {
        Copy-Item -LiteralPath $bombecamFile -Destination (Join-Path $PSScriptRoot "bombecam.exe") -Force
    }

    $commands = @('bombecam-gateway', 'bombecam-net', 'bombecam-policy', 'bombecam-verify', 'bombecam-certgen')
    $builtFiles = @("bombecam" + $extension)
    foreach ($name in $commands) {
        $file = Join-Path $destination ($name + $extension)
        & go build -buildvcs=false -trimpath "-ldflags=-s -w $keyFlag" -o $file "./cmd/$name"
        if ($LASTEXITCODE -ne 0) { throw "Build failed: $name ($Target)" }
        $builtFiles += ($name + $extension)
    }

    $hashes = foreach ($fname in $builtFiles) {
        $fpath = Join-Path $destination $fname
        $hash = (Get-FileHash -LiteralPath $fpath -Algorithm SHA256).Hash.ToLowerInvariant()
        "$hash  $fname"
    }
    $hashes | Set-Content -LiteralPath (Join-Path $destination 'SHA256SUMS.txt') -Encoding ascii
    Write-Output "Built standalone bombecam$extension and backend binaries: $destination"
} finally {
    $env:GOOS = $previousOS
    $env:GOARCH = $previousArch
    $env:CGO_ENABLED = $previousCGO
    Pop-Location
}
