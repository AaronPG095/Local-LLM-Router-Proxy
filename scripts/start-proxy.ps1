param(
    [string]$ConfigPath = ""
)

$ErrorActionPreference = "Stop"
$RepoRoot = Split-Path -Parent $PSScriptRoot
$ExePath = Join-Path $RepoRoot "bin\llm-router.exe"
if (-not $ConfigPath) {
    $ConfigPath = Join-Path $RepoRoot "router.config.yaml"
}

try {
    $health = Invoke-RestMethod -Uri "http://127.0.0.1:8787/healthz" -TimeoutSec 1
    if ($health.status -eq "ok") {
        Write-Output "llm-router is already running on http://127.0.0.1:8787"
        exit 0
    }
} catch {
    # Not running yet; proceed to launch.
}

if (-not (Test-Path $ExePath)) {
    throw "Proxy binary not found at $ExePath. Run: go build -o bin/llm-router.exe ./cmd/router"
}
if (-not (Test-Path $ConfigPath)) {
    throw "Config file not found at $ConfigPath. Copy router.config.example.yaml to router.config.yaml first."
}

$proc = Start-Process -FilePath $ExePath -ArgumentList "-config", $ConfigPath -WorkingDirectory $RepoRoot -PassThru -WindowStyle Hidden

for ($i = 0; $i -lt 25; $i++) {
    try {
        $health = Invoke-RestMethod -Uri "http://127.0.0.1:8787/healthz" -TimeoutSec 1
        if ($health.status -eq "ok") {
            Write-Output "Started llm-router (PID $($proc.Id)) on http://127.0.0.1:8787"
            exit 0
        }
    } catch {
        Start-Sleep -Milliseconds 200
    }
}

throw "llm-router failed to become ready on http://127.0.0.1:8787/healthz"
