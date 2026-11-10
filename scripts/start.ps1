param()
$ErrorActionPreference = 'Stop'
Set-Location (Split-Path -Parent $PSScriptRoot)

if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    throw 'Docker belum ditemukan. Pasang Docker Desktop, aktifkan Linux containers, lalu buka terminal baru.'
}
docker info *> $null
if ($LASTEXITCODE -ne 0) { throw 'Docker belum berjalan. Buka Docker Desktop dan tunggu engine siap.' }

if (-not (Test-Path -LiteralPath '.env')) {
    Copy-Item -LiteralPath '.env.example' -Destination '.env'
}
$envContent = [System.IO.File]::ReadAllText((Join-Path (Get-Location) '.env'))
if ($envContent -notmatch '(?m)^JWT_SECRET=.+\S.*$') {
    $bytes = New-Object byte[] 48
    $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    try { $rng.GetBytes($bytes) } finally { $rng.Dispose() }
    $jwtSecret = [Convert]::ToBase64String($bytes)
    if ($envContent -match '(?m)^JWT_SECRET=.*$') {
        $envContent = [regex]::Replace($envContent, '(?m)^JWT_SECRET=.*$', "JWT_SECRET=$jwtSecret")
    } else {
        $envContent += "`nJWT_SECRET=$jwtSecret`n"
    }
    [System.IO.File]::WriteAllText((Join-Path (Get-Location) '.env'), $envContent, (New-Object System.Text.UTF8Encoding $false))
    Write-Host 'JWT secret acak sudah disimpan di .env.'
}

docker compose up -d --build
if ($LASTEXITCODE -ne 0) { throw 'Startup gagal. Jalankan docker compose logs migrate app mysql kafka.' }

for ($attempt = 0; $attempt -lt 30; $attempt++) {
    try {
        $ready = Invoke-RestMethod 'http://localhost:8080/readyz' -TimeoutSec 4
        Write-Host 'BenzMart siap: http://localhost:8080 (REST API)'
        $ready | Format-List
        Write-Host 'Coba alur auth: powershell -ExecutionPolicy Bypass -File .\scripts\smoke-auth.ps1'
        exit 0
    } catch {
        Start-Sleep -Seconds 2
    }
}
throw 'API belum ready. Periksa: docker compose ps; docker compose logs migrate app mysql kafka redis'
