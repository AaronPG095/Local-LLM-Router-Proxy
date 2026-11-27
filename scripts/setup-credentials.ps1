param(
    [string]$StorePath = (Join-Path $env:LOCALAPPDATA 'LocalLLMRouter/credentials.json')
)

$ErrorActionPreference = 'Stop'
if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw 'Windows DPAPI is required. On other platforms, supply the provider environment variables.'
}

$names = @(
    'GEMINI_API_KEY', 'OPENAI_API_KEY', 'ANTHROPIC_API_KEY',
    'GROQ_API_KEY', 'OPENROUTER_API_KEY', 'MISTRAL_API_KEY'
)
$directory = Split-Path -Parent ([IO.Path]::GetFullPath($StorePath))
$StorePath = [IO.Path]::GetFullPath($StorePath)
$stage = 'checking Windows encryption'
$lock = $null
try {
    $module = Join-Path $PSHOME 'Modules/Microsoft.PowerShell.Security/Microsoft.PowerShell.Security.psd1'
    Import-Module -Name $module -ErrorAction Stop
    $probe = [System.Security.SecureString]::new()
    try {
        $probe.AppendChar('x')
        $protected = Microsoft.PowerShell.Security\ConvertFrom-SecureString -SecureString $probe
        $restored = Microsoft.PowerShell.Security\ConvertTo-SecureString -String $protected
        try {
            if ($restored.Length -ne 1) { throw 'Encryption preflight failed.' }
        } finally { $restored.Dispose() }
    } finally { $probe.Dispose() }

    $stage = 'opening the credential store'
    $null = New-Item -ItemType Directory -Path $directory -Force
    $lock = [IO.File]::Open("$StorePath.lock", 'OpenOrCreate', 'Write', 'None')
    $savedKeys = [ordered]@{}
    if ([IO.File]::Exists($StorePath)) {
        $existing = [IO.File]::ReadAllText($StorePath) | ConvertFrom-Json
        if ($existing -isnot [PSCustomObject]) { throw 'Invalid credential store.' }
        foreach ($item in $existing.PSObject.Properties) {
            if ($item.Value -isnot [string]) { throw 'Invalid credential store entry.' }
            $savedKeys[$item.Name] = $item.Value
        }
    }

    Write-Host 'Key input is masked. Press Enter to keep a saved key or skip an unused provider.'
    foreach ($name in $names) {
        $stage = "saving $name"
        $secret = Read-Host -Prompt $name -AsSecureString
        try {
            if ($secret.Length -eq 0) { continue }
            $savedKeys[$name] = Microsoft.PowerShell.Security\ConvertFrom-SecureString -SecureString $secret
        } finally { $secret.Dispose() }

        $temporary = Join-Path $directory ([IO.Path]::GetRandomFileName())
        try {
            [IO.File]::WriteAllText($temporary, ($savedKeys | ConvertTo-Json))
            if ([IO.File]::Exists($StorePath)) {
                [IO.File]::Replace($temporary, $StorePath, [NullString]::Value)
            } else {
                [IO.File]::Move($temporary, $StorePath)
            }
        } finally {
            if ([IO.File]::Exists($temporary)) { [IO.File]::Delete($temporary) }
        }
    }

    Write-Host 'Credential setup complete. Restart the router to load changes.'
} catch {
    throw "Credential setup failed while $stage. Previously saved keys are still encrypted."
} finally {
    if ($lock) { $lock.Dispose() }
}
