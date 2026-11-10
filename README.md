# BenzMart

Backend platform flash sale dengan Go, MySQL, Kafka (KRaft), dan Redis berdasarkan PRD di repository ini. Implementasi dimulai sebagai modular monolith.

## Fitur yang tersedia

- Migrasi MySQL berversi dengan `golang-migrate`, embedded di binary.
- Registrasi customer dengan UUIDv7 (`BINARY(16)`), email unik, dan bcrypt cost 12.
- Login: JWT HS256 (15 menit) dan refresh token acak (7 hari).
- Refresh token disimpan sebagai hash SHA-256. Rotasi memakai transaksi dan row lock; token lama hanya bisa digunakan satu kali.
- Logout mencabut refresh token. Access token yang sudah diterbitkan tetap berlaku hingga kedaluwarsa (maksimal 15 menit).
- Profil pengguna, pemeriksaan role dari database, dan endpoint admin untuk verifikasi RBAC.
- Rate limit Redis: login 5 percobaan/menit/IP+email, ditambah 30/menit/IP; registrasi 10/menit/IP dan refresh 30/menit/IP. Redis gagal menghasilkan 503.
- Error JSON dengan correlation ID, structured logging, graceful shutdown, health/readiness, Docker Compose, serta CI dengan MySQL 8.4.

Katalog, campaign, checkout, outbox, consumer event Kafka, pembayaran, dan frontend belum tersedia. Kafka sudah disiapkan sebagai infrastruktur dan diperiksa lewat broker metadata; belum ada event bisnis yang dipublikasikan.

## Cara menjalankan di Windows (disarankan)

1. Pasang **Docker Desktop**, gunakan Linux containers, dan tunggu engine berjalan. Go lokal tidak diperlukan.
2. Buka PowerShell di folder proyek:

```powershell
cd 'C:\laragon\www\project go'
powershell -ExecutionPolicy Bypass -File .\scripts\start.ps1
```

Script membuat `.env` jika belum ada, menghasilkan JWT secret acak jika masih kosong, lalu menjalankan MySQL, Kafka, Redis, migrasi, dan API. JWT secret dipertahankan pada startup berikutnya. Build pertama membutuhkan koneksi internet untuk image dan dependency.

3. Periksa dan coba autentikasi:

```powershell
Invoke-RestMethod http://localhost:8080/healthz
Invoke-RestMethod http://localhost:8080/readyz
powershell -ExecutionPolicy Bypass -File .\scripts\smoke-auth.ps1
```

Smoke script mencoba registrasi, login, profil, refresh, penolakan token lama, logout, dan penolakan token yang dicabut. Setiap eksekusi membuat satu akun demo baru.

Alamat `http://localhost:8080` adalah REST API; belum ada halaman toko. Gunakan endpoint di atas, Postman, atau contoh PowerShell di bawah.

## Menjalankan tanpa script

```powershell
Copy-Item .env.example .env
# Edit .env: isi JWT_SECRET dengan secret acak minimal 32 byte.
docker compose up -d --build
docker compose ps -a
```

Migrasi berjalan melalui service `migrate`; `Exited (0)` untuk service itu berarti berhasil. API baru berjalan setelah migrasi selesai. Migrasi yang sudah diterapkan tidak dijalankan ulang. Jangan menyalin ulang `.env.example` di atas `.env` yang sudah dikonfigurasi.

## Contoh request manual

```powershell
$base = 'http://localhost:8080/api/v1'
$register = @{name='Benny'; email='benny@example.com'; password='BenzMart-123!'} | ConvertTo-Json
Invoke-RestMethod "$base/auth/register" -Method Post -ContentType 'application/json' -Body $register

$credentials = @{email='benny@example.com'; password='BenzMart-123!'} | ConvertTo-Json
$tokens = Invoke-RestMethod "$base/auth/login" -Method Post -ContentType 'application/json' -Body $credentials
$headers = @{Authorization="Bearer $($tokens.access_token)"}
Invoke-RestMethod "$base/auth/me" -Headers $headers

$refresh = @{refresh_token=$tokens.refresh_token} | ConvertTo-Json
$tokens = Invoke-RestMethod "$base/auth/refresh" -Method Post -ContentType 'application/json' -Body $refresh

$headers = @{Authorization="Bearer $($tokens.access_token)"}
$logout = @{refresh_token=$tokens.refresh_token} | ConvertTo-Json
Invoke-RestMethod "$base/auth/logout" -Method Post -ContentType 'application/json' -Headers $headers -Body $logout
```

Gunakan email lain jika sudah terdaftar. Setelah refresh, simpan pasangan token baru; token refresh lama akan ditolak. Logout memerlukan access token dan refresh token milik pengguna tersebut.

| Method | Endpoint | Akses |
| --- | --- | --- |
| POST | `/api/v1/auth/register` | Publik; selalu membuat customer |
| POST | `/api/v1/auth/login` | Publik |
| POST | `/api/v1/auth/refresh` | Refresh token valid |
| POST | `/api/v1/auth/logout` | Bearer access token |
| GET | `/api/v1/auth/me` | Bearer access token |
| GET | `/api/v1/admin/me` | Bearer access token dengan role admin |
| GET | `/healthz` | Liveness HTTP |
| GET | `/readyz` | Pemeriksaan MySQL, Kafka, Redis |

Registrasi menerima `name`, `email`, dan `password` (8–72 byte). Field tambahan, termasuk `role`, ditolak. Error memakai `{ "error": { "code", "message", "correlation_id" } }`. Kode utama: 400 validasi, 401 token/kredensial, 403 role, 409 email terdaftar, 429 rate limit, 503 dependency gagal.

## Membuat admin

Tidak ada akun atau password admin bawaan. Setelah startup berhasil, siapkan environment untuk satu perintah:

```powershell
$env:ADMIN_NAME = 'Admin BenzMart'
$env:ADMIN_EMAIL = 'admin@example.com'
$secure = Read-Host 'Password admin (8–72 byte)' -AsSecureString
$pointer = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secure)
try { $env:ADMIN_PASSWORD = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($pointer) }
finally { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($pointer) }
try {
    docker compose run --rm -e ADMIN_NAME -e ADMIN_EMAIL -e ADMIN_PASSWORD app -create-admin
} finally {
    Remove-Item Env:ADMIN_PASSWORD
}
```

Perintah membuat akun baru dan menolak email yang sudah digunakan. Login melalui endpoint biasa, lalu akses `/api/v1/admin/me` dengan token admin. Endpoint tersebut hanya memverifikasi RBAC; dashboard belum tersedia.

## Port dan troubleshooting

| Komponen | Host | Di jaringan Compose |
| --- | --- | --- |
| API | `localhost:8080` | `app:8080` |
| MySQL | `localhost:3307` | `mysql:3306` |
| Kafka | `localhost:9092` | `kafka:29092` |
| Redis | `localhost:6379` | `redis:6379` |

MySQL memakai port 3307 agar tidak bentrok dengan MySQL Laragon. Jika port lain terpakai, hentikan aplikasi yang memakainya atau ubah mapping Compose. Jika mengganti port API, sesuaikan URL request dan parameter `-BaseUrl` pada smoke script.

```powershell
docker compose ps -a
docker compose logs --tail 100 migrate app mysql kafka redis
docker compose down
```

`down` mempertahankan volume data. Perubahan password MySQL di `.env` tidak otomatis mengganti akun dalam volume yang sudah dibuat. Jika migrasi gagal dan ditandai dirty, periksa kegagalan serta schema terlebih dahulu; jangan memaksa versi tanpa memperbaiki keadaan database.

Konfigurasi Compose ini untuk development lokal (port hanya loopback, password contoh, Kafka/Redis plaintext). Gunakan TLS, kredensial terpisah, dan pengaturan proxy yang tepercaya sebelum deployment publik. Rate limit memakai alamat koneksi langsung; header proxy belum dipercaya.

## Menjalankan Go di host

Pasang Go 1.26.x. Aplikasi membaca environment proses, bukan file `.env` secara otomatis.

```powershell
docker compose up -d mysql kafka redis
$env:MYSQL_DSN = 'benzmart:benzmart_local@tcp(localhost:3307)/benzmart?parseTime=true&loc=UTC&timeout=3s&readTimeout=3s&writeTimeout=3s'
$env:JWT_SECRET = 'isi-secret-acak-minimal-32-byte-anda'
$env:KAFKA_BROKERS = 'localhost:9092'
$env:REDIS_ADDR = 'localhost:6379'
go mod download
go run ./cmd/monolith -migrate
go run ./cmd/monolith
```

Isi `.env` untuk Compose terlebih dahulu, walaupun menjalankan Go di host. Sesuaikan DSN dengan kredensial Anda. Untuk admin di host gunakan environment `ADMIN_*` yang sama dan `go run ./cmd/monolith -create-admin`.

## Pengujian

```powershell
go test ./...
go vet ./...
go build -o bin/benzmart.exe ./cmd/monolith
```

Unit test mencakup validasi, hashing password, JWT salah/kedaluwarsa, RBAC, body JSON, error, rate limit, dan health/readiness. Rate limit diuji memakai emulator Redis dalam proses. Integration test MySQL bersifat opt-in:

```powershell
# Buat database benzmart_auth_test terpisah terlebih dahulu.
$env:BENZMART_TEST_MYSQL_DSN = 'benzmart:benzmart_local@tcp(localhost:3307)/benzmart_auth_test?parseTime=true&loc=UTC&timeout=3s'
go test -count=1 -v ./internal/core/auth
```

User MySQL untuk tes harus mempunyai izin ke database test tersebut; user Compose biasa hanya mendapat izin ke database aplikasi secara default. Test menolak nama database yang tidak berakhiran `_test`, menerapkan migrasi, dan membersihkan hanya akun serta token yang dibuatnya. Skenario integrasi mencakup registrasi/login HTTP, email ganda, delapan refresh bersamaan (tepat satu berhasil), replay, logout, dan token kedaluwarsa. CI menjalankan tes tersebut pada MySQL 8.4 dengan race detector.

Struktur utama: `cmd/monolith` (entry point), `internal/core/auth` (HTTP/service/repository), `internal/platform` (config/health), `migrations` (SQL embedded), dan `scripts` (startup/smoke).

Validasi lokal (10 November 2026): build Windows, `go vet`, unit test, integration test dengan MySQL 8.0.30 Laragon pada instance terpisah, serta smoke script terhadap binary API dan Redis lokal berhasil. Coverage modul auth 84,2%. Startup penuh Docker Compose dan Kafka belum dijalankan karena Docker Desktop tidak tersedia pada sesi ini. CI untuk target MySQL 8.4 sudah disiapkan tetapi belum dijalankan di GitHub.

Volume Kafka memakai `/var/lib/kafka/data`, direktori data yang disiapkan untuk user aplikasi oleh [image resmi Apache Kafka](https://github.com/apache/kafka/blob/3.9.1/docker/jvm/Dockerfile).
