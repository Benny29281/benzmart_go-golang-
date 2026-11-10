param([string]$BaseUrl = 'http://localhost:8080')
$ErrorActionPreference = 'Stop'
$email = "demo-$([guid]::NewGuid().ToString('N'))@example.com"
$password = 'BenzMart-demo-123!'

function Send-Json([string]$Path, $Data, $Headers = @{}) {
    Invoke-RestMethod -Uri "$BaseUrl/api/v1$Path" -Method Post -ContentType 'application/json' -Body ($Data | ConvertTo-Json -Compress) -Headers $Headers
}
function Expect-Rejected([string]$Path, $Data) {
    try {
        Send-Json $Path $Data | Out-Null
    } catch {
        if ($null -ne $_.Exception.Response -and [int]$_.Exception.Response.StatusCode -eq 401) { return }
        throw
    }
    throw "Token seharusnya ditolak: $Path"
}

$user = Send-Json '/auth/register' @{name='Demo BenzMart'; email=$email; password=$password}
Write-Host "Registrasi berhasil: $($user.user.email)"
$login = Send-Json '/auth/login' @{email=$email; password=$password}
$profile = Invoke-RestMethod "$BaseUrl/api/v1/auth/me" -Headers @{Authorization="Bearer $($login.access_token)"}
Write-Host "Login dan profil berhasil. Role: $($profile.user.role)"
$next = Send-Json '/auth/refresh' @{refresh_token=$login.refresh_token}
Expect-Rejected '/auth/refresh' @{refresh_token=$login.refresh_token}
Write-Host 'Refresh berhasil; token lama ditolak.'
Send-Json '/auth/logout' @{refresh_token=$next.refresh_token} @{Authorization="Bearer $($next.access_token)"} | Out-Null
Expect-Rejected '/auth/refresh' @{refresh_token=$next.refresh_token}
Write-Host 'Logout berhasil; refresh token sudah dicabut.'
Write-Host "Selesai. Akun demo $email tetap tersimpan di database lokal."
