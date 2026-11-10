# PRD: BenzMart – Flash Sale E-Commerce Platform

| Item | Detail |
| --- | --- |
| Versi | 2.0 (revisi dari FlashMart v1.0) |
| Status | Draft untuk review |
| Tanggal | 10 November 2026 |
| Stack | Go 1.22+, MySQL 8.4 (InnoDB), Apache Kafka (KRaft), Redis |
| Tipe Proyek | Event-driven web application (portofolio) |

---

## 0. Riwayat Revisi & Ringkasan Perubahan

PRD ini merevisi PRD "FlashMart" sebelumnya. Perubahan utama:

| # | Perubahan | Alasan |
| --- | --- | --- |
| 1 | Nama proyek **FlashMart → BenzMart** | Sesuai keputusan branding |
| 2 | Database **PostgreSQL → MySQL 8.4 (InnoDB)** | Sesuai stack pilihan; seluruh model data, query, migrasi, dan driver disesuaikan (UUID `BINARY(16)`, tipe `JSON`, tanpa `RETURNING`, `SKIP LOCKED`) |
| 3 | Pertanyaan terbuka dijawab jadi **keputusan desain** (bagian 18) | Agar tim tidak ragu saat implementasi |
| 4 | Arsitektur disederhanakan: **modular monolith dulu, lalu dipecah** menjadi 6 service | Menekan kompleksitas, tetap menunjukkan praktik event-driven |
| 5 | Order v1 dibatasi **1 produk per order** (satu `flash_sale_id`) | Menyederhanakan saga dan partition key |
| 6 | Ditambahkan **Redis stock gate** (pre-check atomik via Lua) | Melindungi MySQL dari lonjakan saat flash sale |
| 7 | Ditambahkan **konfirmasi stok** setelah pembayaran (`RESERVED → CONFIRMED`) yang sebelumnya tidak ada | Flow stok pada PRD lama tidak lengkap |
| 8 | Ditambahkan **penegakan batas beli per user**, **scheduler timeout order**, dan **penanganan race** (bayar vs timeout) | Celah logika pada PRD lama |
| 9 | Ditambahkan **spesifikasi error API, state machine, DDL MySQL, konvensi event, testing & definisi selesai (DoD)** | Kelengkapan dokumen |
| 10 | Harga disimpan sebagai **BIGINT (Rupiah, tanpa desimal)** | Hindari masalah floating point |

---

## 1. Ringkasan

**BenzMart** adalah website e-commerce dengan fitur utama **flash sale**: produk dijual dengan harga diskon, stok terbatas, dan periode singkat. Saat flash sale dimulai, trafik melonjak dalam hitungan detik. Sistem harus tetap cepat, **tidak menjual melebihi stok (no overselling)**, dan memproses pesanan secara andal.

Kafka menjadi tulang punggung komunikasi antar-layanan sehingga pembuatan order, reservasi stok, pembayaran, dan notifikasi berjalan **asinkron, terpisah, dan tahan kegagalan**. Backend ditulis dengan Go, data disimpan di MySQL.

## 2. Latar Belakang & Masalah

- Flash sale menyebabkan lonjakan request dalam hitungan detik.
- Pemrosesan sinkron (order → stok → bayar → notifikasi dalam satu request) lambat dan gagal total jika satu bagian bermasalah.
- Race condition pada stok dapat menyebabkan overselling.
- Pengguna butuh kepastian status order secara real-time.
- Satu instance MySQL mudah menjadi bottleneck jika semua request langsung menulis ke baris stok yang sama (hot row).

## 3. Tujuan (Goals)

1. Memproses order flash sale secara asinkron dan andal menggunakan Kafka.
2. Menjamin **tidak ada overselling** walau request bersamaan.
3. Respons cepat: order diterima **p95 < 300 ms** pada 1.000 RPS.
4. Arsitektur mudah di-scale horizontal (service stateless, consumer group).
5. Menjadi proyek portofolio yang menunjukkan praktik backend modern: outbox, idempotency, saga, DLQ, observability.

## 4. Non-Goals (Di Luar Cakupan v1)

- Payment gateway sungguhan (v1 memakai **payment simulator**).
- Aplikasi mobile native.
- Rekomendasi produk berbasis ML.
- Multi-currency dan multi-bahasa (v1: IDR, Bahasa Indonesia).
- Sistem logistik/pengiriman (hanya status order).
- Keranjang belanja multi-item dan pembelian produk non-flash-sale (produk reguler hanya ditampilkan di katalog).
- Refund sungguhan (hanya penandaan status refund simulasi).

## 5. Target Pengguna

| Persona | Deskripsi | Kebutuhan Utama |
| --- | --- | --- |
| **Customer** | Pembeli yang mengikuti flash sale | Checkout cepat, status order jelas |
| **Admin/Seller** | Pengelola produk dan campaign | Kelola produk, stok, jadwal flash sale, lihat laporan |

## 6. User Stories

**Customer**

- Mendaftar dan login agar bisa berbelanja.
- Melihat daftar flash sale aktif dan mendatang beserta countdown.
- Membeli produk flash sale dan langsung mendapat konfirmasi bahwa order sedang diproses.
- Melihat status order: `PENDING`, `STOCK_RESERVED`, `PAID`, `FAILED`, `CANCELLED`.
- Menerima notifikasi (email dan in-app) ketika order berhasil atau gagal.
- Mendapat pesan jelas jika stok habis atau batas beli tercapai.

**Admin**

- Membuat/mengubah produk dan stok awal.
- Menjadwalkan campaign flash sale (waktu mulai/selesai, harga, kuota, batas per user).
- Melihat dashboard penjualan dan daftar order.

## 7. Kebutuhan Fungsional

### 7.1 Autentikasi & Pengguna (FR-AUTH)

- FR-AUTH-1: Registrasi dan login dengan email + password (hash **argon2id** atau bcrypt cost ≥ 12).
- FR-AUTH-2: JWT access token (15 menit) + refresh token (7 hari, disimpan hash-nya, bisa dicabut).
- FR-AUTH-3: RBAC dengan role `customer` dan `admin`.
- FR-AUTH-4: Rate limit login (mis. 5 percobaan/menit/IP+email).

### 7.2 Katalog Produk (FR-CAT)

- FR-CAT-1: CRUD produk (admin).
- FR-CAT-2: Daftar & detail produk dengan pagination dan pencarian (`FULLTEXT` index MySQL atau `LIKE` prefix pada v1).
- FR-CAT-3: Cache detail produk dan daftar flash sale aktif di Redis (TTL pendek, invalidasi saat admin mengubah data).

### 7.3 Flash Sale (FR-FS)

- FR-FS-1: Admin membuat campaign: produk, harga flash, kuota, batas per user, waktu mulai & selesai.
- FR-FS-2: Status campaign otomatis `SCHEDULED → ACTIVE → ENDED` oleh scheduler; validasi waktu juga dilakukan saat checkout (jangan hanya percaya status).
- FR-FS-3: Batas beli per user per campaign (default 1, maksimum diatur admin, tidak lebih dari 5).
- FR-FS-4: Saat campaign dibuat/diaktifkan, kuota dimuat ke Redis (`stock:{flash_sale_id}`) dan ke tabel stok inventory.
- FR-FS-5: Campaign yang berstatus `ACTIVE` tidak boleh diubah harga/kuotanya (hanya bisa dibatalkan).

### 7.4 Order (FR-ORD)

- FR-ORD-1: `POST /orders` memvalidasi (auth, campaign aktif, quantity, batas per user), menyimpan order `PENDING` + baris `outbox` dalam **satu transaksi MySQL**, lalu merespons `202 Accepted` dengan `order_id`.
- FR-ORD-2: Wajib header `Idempotency-Key`. Key yang sama dari user yang sama mengembalikan order yang sama (bukan order baru).
- FR-ORD-3: Status order diperbarui hanya berdasarkan event, dengan **update bersyarat** (lihat state machine, bagian 9.6).
- FR-ORD-4: Customer dapat melihat riwayat dan detail order miliknya.
- FR-ORD-5: Order `STOCK_RESERVED` yang tidak dibayar dalam **15 menit** otomatis `CANCELLED` (alasan `PAYMENT_TIMEOUT`) dan stok dikembalikan.
- FR-ORD-6: Customer dapat membatalkan order selama belum `PAID`.
- FR-ORD-7: Counter batas beli per user dikembalikan saat order `FAILED`/`CANCELLED`.

### 7.5 Inventory (FR-INV)

- FR-INV-1: Mengonsumsi `order.created`, mereservasi stok **atomik** di MySQL.
- FR-INV-2: Stok cukup → publikasikan `inventory.reserved`; tidak cukup → `inventory.rejected`.
- FR-INV-3: Mengonsumsi `payment.succeeded` → ubah reservasi `RESERVED → CONFIRMED` (stok resmi terjual).
- FR-INV-4: Mengonsumsi `order.cancelled` / `payment.failed` → ubah reservasi `RESERVED → RELEASED` dan kembalikan stok (idempoten; tidak boleh melepas dua kali).
- FR-INV-5: Pelepasan stok juga menambah kembali counter di Redis.

### 7.6 Payment (FR-PAY)

- FR-PAY-1: Mengonsumsi `inventory.reserved`, membuat tagihan simulasi (`PENDING`, kedaluwarsa 15 menit).
- FR-PAY-2: Endpoint simulator untuk menandai pembayaran sukses/gagal.
- FR-PAY-3: Publikasikan `payment.succeeded` atau `payment.failed`.
- FR-PAY-4: Jika pembayaran sukses tiba setelah order sudah `CANCELLED`, tandai payment `REFUND_REQUIRED` dan publikasikan `payment.refund_required` (simulasi).

### 7.7 Notifikasi (FR-NOTIF)

- FR-NOTIF-1: Mengonsumsi event order/payment; kirim email (SMTP/Mailhog) dan notifikasi in-app.
- FR-NOTIF-2: Retry dengan exponential backoff; gagal permanen masuk Dead Letter Topic (DLT).
- FR-NOTIF-3: Email idempoten (tidak terkirim dua kali untuk event yang sama).

### 7.8 Real-time Status (FR-RT)

- FR-RT-1: Frontend menerima pembaruan status order lewat **Server-Sent Events (SSE)**.
- FR-RT-2: Koneksi SSE otomatis menutup saat status final; ada heartbeat setiap 15 detik.

### 7.9 Admin Dashboard (FR-ADM)

- FR-ADM-1: Ringkasan: total order, omzet, order sukses/gagal, stok tersisa per campaign.
- FR-ADM-2: Daftar order dengan filter status, campaign, dan tanggal.
- FR-ADM-3: Melihat dan melakukan replay pesan di DLT (minimal lewat CLI/Kafka UI pada v1).

## 8. Kebutuhan Non-Fungsional

| Kategori | Target |
| --- | --- |
| Performa | `POST /orders` p95 < 300 ms pada 1.000 RPS |
| Konsistensi | Tidak ada overselling; `available_stock` tidak pernah negatif (dijaga query atomik + `CHECK` constraint MySQL 8.0.16+) |
| Ketersediaan | Kegagalan satu consumer tidak menghentikan checkout |
| Keandalan | Delivery event *at-least-once* + consumer idempoten (efek akhir *effectively-once*) |
| Keamanan | Hash password kuat, validasi input, rate limiting, prepared statement (anti SQL injection), CORS ketat, secret via env, TLS di produksi |
| Observability | Structured logging (`slog`), metrics Prometheus, tracing OpenTelemetry, `correlation_id` di setiap request dan event |
| Skalabilitas | Service stateless, scale via consumer group, partisi Kafka cukup |
| Testing | Coverage ≥ 70% untuk logic inti; integration test dengan `testcontainers-go` (MySQL + Kafka); load test `k6` |
| Data | `utf8mb4`, timezone UTC di DB, waktu ditampilkan WIB di UI |
| Backup | Dump MySQL terjadwal (cukup dokumentasi untuk v1) |

## 9. Arsitektur Sistem

### 9.1 Gambaran

```
 Browser ──► Gateway (Go) ──┬──► Core Service (user, catalog, flash sale, admin)
               │ Redis       └──► Order Service
               │ (rate limit,            │
               │  stock gate)            ▼  Transactional Outbox
               │              ┌────────── KAFKA ───────────┐
               │              │ order.created, inventory.* │
               │              │ payment.*, order.cancelled │
               │              └──┬──────────┬───────────┬──┘
               │                 ▼          ▼           ▼
               │          Inventory Svc  Payment Svc  Notification Svc
               ▼                 │          │           │
          SSE ke browser      MySQL      MySQL     SMTP / in-app
```

### 9.2 Layanan

| Service | Tanggung Jawab | Schema MySQL |
| --- | --- | --- |
| `gateway` | Routing, auth middleware, rate limit, Redis stock gate, serve frontend, endpoint SSE | – |
| `core-service` | User, auth, katalog produk, campaign flash sale, endpoint admin | `benzmart_core` |
| `order-service` | Checkout, status order, outbox publisher, scheduler timeout | `benzmart_orders` |
| `inventory-service` | Reservasi, konfirmasi, dan pelepasan stok | `benzmart_inventory` |
| `payment-service` | Simulasi pembayaran | `benzmart_payments` |
| `notification-service` | Email & notifikasi in-app | `benzmart_notif` |

Prinsip: **database per service** (pada dev boleh satu server MySQL dengan beberapa schema dan user terpisah), tidak ada query lintas schema. Komunikasi lewat Kafka (async) atau REST/gRPC (query sinkron seperti validasi campaign).

> **Strategi bertahap:** Fase 1 dibangun sebagai **modular monolith** (satu repo, satu binary, modul `internal/*` terpisah dengan antarmuka jelas). Pemecahan menjadi service terpisah dilakukan di Fase 2–3 tanpa mengubah kontrak event.

### 9.3 Alur Utama (Saga Choreography)

1. **Gateway** menjalankan rate limit dan **Redis stock gate**: `DECR` atomik via Lua pada `stock:{flash_sale_id}`. Jika hasil < 0, langsung balas `409 SOLD_OUT` tanpa menyentuh MySQL.
2. Customer menekan **Beli** → `order-service` memvalidasi, mengecek batas per user, menyimpan order `PENDING` + `outbox` dalam satu transaksi, membalas `202`.
3. Outbox publisher mengirim `order.created` ke Kafka.
4. `inventory-service` mereservasi stok (query atomik, bagian 9.5):
   - Sukses → `inventory.reserved`
   - Gagal → `inventory.rejected` → order `FAILED`, Redis stock dikembalikan.
5. `order-service` menerima `inventory.reserved` → status `STOCK_RESERVED` dan mencatat `reserved_at`.
6. `payment-service` menerima `inventory.reserved` → membuat tagihan → customer membayar (simulator) → `payment.succeeded` / `payment.failed`.
7. `payment.succeeded` → order `PAID`, inventory `CONFIRMED`. `payment.failed` → order `CANCELLED` + stok dilepas.
8. Scheduler `order-service` membatalkan order `STOCK_RESERVED` > 15 menit → `order.cancelled` → stok dilepas.
9. `notification-service` mengirim notifikasi pada setiap hasil akhir.
10. Gateway mendorong perubahan status ke browser via SSE.

### 9.4 Topic Kafka

Konvensi nama: `benzmart.<domain>.<event>`.

| Topic | Producer | Consumer | Key | Partisi (dev/prod) |
| --- | --- | --- | --- | --- |
| `benzmart.order.created` | order-service | inventory-service | `flash_sale_id` | 6 / 12 |
| `benzmart.inventory.reserved` | inventory-service | order-service, payment-service | `order_id` | 6 / 12 |
| `benzmart.inventory.rejected` | inventory-service | order-service, notification-service | `order_id` | 3 / 6 |
| `benzmart.payment.succeeded` | payment-service | order-service, inventory-service, notification-service | `order_id` | 3 / 6 |
| `benzmart.payment.failed` | payment-service | order-service, inventory-service, notification-service | `order_id` | 3 / 6 |
| `benzmart.payment.refund_required` | payment-service | notification-service | `order_id` | 1 / 3 |
| `benzmart.order.cancelled` | order-service | inventory-service, notification-service | `order_id` | 3 / 6 |
| `<topic>.dlt` | semua consumer | admin/tooling | sama | 1 / 3 |

Catatan desain:

- Key `flash_sale_id` pada `order.created` membuat order untuk campaign yang sama diproses **berurutan per partisi**, mengurangi kontensi baris stok.
- Key `order_id` pada event lain menjaga urutan event per order.
- Producer: `acks=all`, `enable.idempotence=true`. Consumer: commit offset **setelah** proses DB selesai (manual commit).
- Format pesan: JSON v1 dengan envelope standar:

```json
{
  "event_id": "0192f3a1-7c1e-7e2b-9d3a-1b2c3d4e5f60",
  "event_type": "order.created",
  "version": 1,
  "occurred_at": "2026-11-10T10:00:00Z",
  "correlation_id": "uuid",
  "producer": "order-service",
  "data": {
    "order_id": "uuid",
    "user_id": "uuid",
    "flash_sale_id": "uuid",
    "product_id": "uuid",
    "quantity": 1,
    "total_amount": 1500000
  }
}
```

Aturan evolusi skema: hanya boleh **menambah** field opsional; perubahan breaking menaikkan `version` dan memakai topic/handler baru.

### 9.5 Pola Keandalan

- **Transactional Outbox**: event ditulis ke tabel `outbox` dalam transaksi yang sama dengan perubahan data. Publisher polling memakai `SELECT ... WHERE published_at IS NULL ORDER BY id LIMIT N FOR UPDATE SKIP LOCKED` (MySQL 8+) agar beberapa instance publisher tidak saling bentrok. Opsi lanjutan: CDC dengan Debezium.
- **Idempotent Consumer**: tabel `processed_events (consumer, event_id)` dengan primary key gabungan; insert dilakukan **dalam transaksi yang sama** dengan efek bisnis. Duplikat → `Error 1062` → abaikan dan commit offset.
- **Reservasi stok atomik** (tanpa lock eksplisit):

```sql
UPDATE flash_sale_stocks
   SET available = available - ?, reserved = reserved + ?
 WHERE flash_sale_id = ? AND available >= ?;
-- RowsAffected = 1 → reservasi sukses; 0 → stok habis
```

- **Retry deadlock**: MySQL dapat mengembalikan `Error 1213 (deadlock)` / `1205 (lock wait timeout)`; semua transaksi kritikal dibungkus helper retry maksimal 3 kali dengan jitter.
- **Retry + Backoff + DLT**: consumer retry 3–5 kali (backoff eksponensial), lalu kirim ke `<topic>.dlt` dengan header error.
- **Redis stock gate** hanyalah *filter awal*; **sumber kebenaran stok tetap MySQL**. Jika Redis hilang, stok dimuat ulang dari MySQL sebelum campaign dibuka kembali.

### 9.6 State Machine Order

```
PENDING ──inventory.reserved──► STOCK_RESERVED ──payment.succeeded──► PAID
   │                                  │
   │ inventory.rejected               ├─ payment.failed / timeout / cancel ─► CANCELLED
   ▼                                  │
 FAILED                               ▼
                                  (stok dilepas)
```

Aturan: perubahan status selalu memakai update bersyarat, mis.

```sql
UPDATE orders SET status='PAID', updated_at=NOW(3)
 WHERE id=? AND status='STOCK_RESERVED';
```

`RowsAffected = 0` berarti transisi tidak valid (mis. order sudah `CANCELLED`) → event dicatat dan ditangani sesuai aturan (contoh: `payment.succeeded` pada order `CANCELLED` memicu `payment.refund_required`). Status final (`PAID`, `FAILED`, `CANCELLED`) tidak bisa berubah lagi.

## 10. Model Data (MySQL 8.4, InnoDB, utf8mb4)

Konvensi:

- ID memakai **UUIDv7** yang dibuat di aplikasi (terurut waktu, ramah index) dan disimpan sebagai `BINARY(16)`; di API ditampilkan sebagai string.
- Uang dalam `BIGINT` (Rupiah). Waktu memakai `DATETIME(3)` UTC.
- Karena MySQL tidak punya `RETURNING`, ID dibuat di aplikasi sebelum insert.
- Semua foreign key lintas service **tidak** dipakai (hanya referensi logis).

### 10.1 Schema `benzmart_core`

**users**: `id`, `email` (unique), `password_hash`, `name`, `role` (`customer|admin`), `created_at`, `updated_at`

**refresh\_tokens**: `id`, `user_id`, `token_hash` (unique), `expires_at`, `revoked_at`

**products**: `id`, `name`, `description`, `price`, `image_url`, `is_active`, `created_at`, `updated_at` (+ `FULLTEXT(name, description)`)

**flash\_sales**: `id`, `product_id`, `flash_price`, `quota`, `max_per_user`, `starts_at`, `ends_at`, `status` (`SCHEDULED|ACTIVE|ENDED|CANCELLED`), `created_at`; index `(status, starts_at)`

### 10.2 Schema `benzmart_orders`

```sql
CREATE TABLE orders (
  id              BINARY(16) PRIMARY KEY,
  user_id         BINARY(16) NOT NULL,
  flash_sale_id   BINARY(16) NOT NULL,
  product_id      BINARY(16) NOT NULL,
  quantity        INT NOT NULL CHECK (quantity > 0),
  unit_price      BIGINT NOT NULL,
  total_amount    BIGINT NOT NULL,
  status          VARCHAR(20) NOT NULL,
  cancel_reason   VARCHAR(40) NULL,
  idempotency_key VARCHAR(64) NOT NULL,
  reserved_at     DATETIME(3) NULL,
  created_at      DATETIME(3) NOT NULL,
  updated_at      DATETIME(3) NOT NULL,
  UNIQUE KEY uq_user_idem (user_id, idempotency_key),
  KEY idx_user_created (user_id, created_at),
  KEY idx_status_reserved (status, reserved_at)
) ENGINE=InnoDB;

CREATE TABLE user_purchase_counters (
  user_id       BINARY(16) NOT NULL,
  flash_sale_id BINARY(16) NOT NULL,
  qty           INT NOT NULL DEFAULT 0,
  PRIMARY KEY (user_id, flash_sale_id)
) ENGINE=InnoDB;

CREATE TABLE outbox (
  id           BIGINT AUTO_INCREMENT PRIMARY KEY,
  event_id     BINARY(16) NOT NULL UNIQUE,
  topic        VARCHAR(100) NOT NULL,
  msg_key      VARCHAR(64) NOT NULL,
  payload      JSON NOT NULL,
  created_at   DATETIME(3) NOT NULL,
  published_at DATETIME(3) NULL,
  KEY idx_unpublished (published_at, id)
) ENGINE=InnoDB;

CREATE TABLE processed_events (
  consumer     VARCHAR(60) NOT NULL,
  event_id     BINARY(16) NOT NULL,
  processed_at DATETIME(3) NOT NULL,
  PRIMARY KEY (consumer, event_id)
) ENGINE=InnoDB;
```

Batas beli per user ditegakkan atomik saat checkout (dalam transaksi yang sama dengan insert order):

```sql
INSERT INTO user_purchase_counters (user_id, flash_sale_id, qty) VALUES (?, ?, 0)
  ON DUPLICATE KEY UPDATE qty = qty;
UPDATE user_purchase_counters SET qty = qty + ?
 WHERE user_id = ? AND flash_sale_id = ? AND qty + ? <= ?;  -- max_per_user
-- RowsAffected = 0 → 422 LIMIT_EXCEEDED
```

### 10.3 Schema `benzmart_inventory`

```sql
CREATE TABLE flash_sale_stocks (
  flash_sale_id BINARY(16) PRIMARY KEY,
  product_id    BINARY(16) NOT NULL,
  total_quota   INT NOT NULL,
  available     INT NOT NULL,
  reserved      INT NOT NULL DEFAULT 0,
  sold          INT NOT NULL DEFAULT 0,
  updated_at    DATETIME(3) NOT NULL,
  CONSTRAINT chk_stock CHECK (available >= 0 AND reserved >= 0 AND sold >= 0
                              AND available + reserved + sold = total_quota)
) ENGINE=InnoDB;

CREATE TABLE stock_reservations (
  id            BINARY(16) PRIMARY KEY,
  order_id      BINARY(16) NOT NULL UNIQUE,
  flash_sale_id BINARY(16) NOT NULL,
  quantity      INT NOT NULL,
  status        VARCHAR(12) NOT NULL,  -- RESERVED | CONFIRMED | RELEASED
  created_at    DATETIME(3) NOT NULL,
  updated_at    DATETIME(3) NOT NULL
) ENGINE=InnoDB;
-- plus tabel processed_events
```

Invarian: `available + reserved + sold = total_quota` selalu terjaga, dan dapat dipakai sebagai pengecekan audit.

### 10.4 Schema `benzmart_payments`

**payments**: `id`, `order_id` (unique), `amount`, `status` (`PENDING|SUCCEEDED|FAILED|EXPIRED|REFUND_REQUIRED`), `expires_at`, `paid_at`, `created_at`, `updated_at` + `processed_events`

### 10.5 Schema `benzmart_notif`

**notifications**: `id`, `user_id`, `order_id`, `type`, `title`, `body`, `channel` (`EMAIL|IN_APP`), `status`, `read_at`, `created_at`; unique `(order_id, type, channel)` untuk mencegah kirim ganda + `processed_events`

## 11. Rancangan API (REST)

Prefix `/api/v1`. Respons error seragam:

```json
{ "error": { "code": "SOLD_OUT", "message": "Stok flash sale habis", "correlation_id": "uuid" } }
```

| Method | Endpoint | Deskripsi | Auth |
| --- | --- | --- | --- |
| POST | `/auth/register` | Registrasi | – |
| POST | `/auth/login` | Login, dapatkan token | – |
| POST | `/auth/refresh` | Refresh token | – |
| POST | `/auth/logout` | Cabut refresh token | Customer |
| GET | `/products` | Daftar produk (`?q=&page=&limit=`) | – |
| GET | `/products/{id}` | Detail produk | – |
| GET | `/flash-sales` | Flash sale aktif/mendatang | – |
| GET | `/flash-sales/{id}` | Detail campaign + sisa stok (perkiraan dari Redis) | – |
| POST | `/orders` | Checkout (header `Idempotency-Key`) | Customer |
| GET | `/orders` | Riwayat order | Customer |
| GET | `/orders/{id}` | Detail & status order | Customer |
| GET | `/orders/{id}/events` | Stream status (SSE) | Customer |
| POST | `/orders/{id}/cancel` | Batalkan order | Customer |
| POST | `/payments/{order_id}/simulate` | Simulasi bayar (`{"result":"success"\|"failed"}`) | Customer |
| GET | `/notifications` | Notifikasi in-app | Customer |
| POST | `/admin/products` | Buat produk | Admin |
| PUT | `/admin/products/{id}` | Ubah produk | Admin |
| POST | `/admin/flash-sales` | Buat campaign | Admin |
| POST | `/admin/flash-sales/{id}/cancel` | Batalkan campaign | Admin |
| GET | `/admin/orders` | Daftar order (filter) | Admin |
| GET | `/admin/dashboard` | Statistik | Admin |
| GET | `/healthz`, `/readyz`, `/metrics` | Health & metrics | Internal |

Contoh `POST /orders`:

```json
// Request (header: Idempotency-Key: 7b1f...)
{ "flash_sale_id": "uuid", "quantity": 1 }

// Response 202
{ "order_id": "uuid", "status": "PENDING" }
```

Kode error utama:

| HTTP | Code | Kondisi |
| --- | --- | --- |
| 400 | `VALIDATION_ERROR` | Input tidak valid / `Idempotency-Key` hilang |
| 401 | `UNAUTHORIZED` | Token tidak valid/kedaluwarsa |
| 403 | `FORBIDDEN` | Role tidak sesuai |
| 404 | `NOT_FOUND` | Resource tidak ada |
| 409 | `SOLD_OUT` | Stok habis (Redis gate / inventory) |
| 409 | `FLASH_SALE_NOT_ACTIVE` | Campaign belum mulai / sudah berakhir |
| 422 | `LIMIT_EXCEEDED` | Melebihi batas beli per user |
| 429 | `RATE_LIMITED` | Terlalu banyak request |

## 12. Tech Stack

| Kebutuhan | Pilihan |
| --- | --- |
| Bahasa | Go 1.22+ |
| HTTP Router | `chi` |
| Database | **MySQL 8.4 LTS** (InnoDB) |
| Driver & akses data | `go-sql-driver/mysql` + `sqlc` (engine `mysql`) + `sqlx`/`database/sql` untuk transaksi |
| Migrasi | `golang-migrate` (driver `mysql`) |
| Kafka Client | `segmentio/kafka-go` (atau `franz-go` untuk kontrol lebih) |
| Cache / Rate limit / Stock gate | Redis 7 + `go-redis` (script Lua) |
| ID | UUIDv7 (`google/uuid` v1.6+) |
| Frontend | `html/template` + HTMX + Alpine.js (server-rendered) |
| Auth | `golang-jwt/jwt` + `argon2id` |
| Config | env variables + `envconfig`/`viper` |
| Logging | `slog` |
| Observability | Prometheus, Grafana, OpenTelemetry, Jaeger |
| Testing | `testing`, `testify`, `testcontainers-go`, `k6` |
| Infrastruktur lokal | Docker Compose: MySQL, Kafka (KRaft), Redis, Mailhog, Kafka UI, Prometheus, Grafana, Jaeger |
| CI/CD | GitHub Actions (lint `golangci-lint`, test, build image) |

## 13. Struktur Repository

```
benzmart/
├── cmd/
│   ├── gateway/
│   ├── core-service/
│   ├── order-service/
│   ├── inventory-service/
│   ├── payment-service/
│   ├── notification-service/
│   └── monolith/            # mode awal (Fase 1)
├── internal/
│   ├── core/       (user, catalog, flashsale | handler, service, repository, domain)
│   ├── order/
│   ├── inventory/
│   ├── payment/
│   ├── notification/
│   └── platform/   (kafka, mysql, redis, outbox, idempotency, logger, config, middleware, otel)
├── migrations/     (per service: core/, orders/, inventory/, payments/, notif/)
├── web/            (templates & static)
├── deploy/         (docker-compose.yml, prometheus/, grafana/, k8s/)
├── test/           (integration & k6 scripts)
├── docs/           (ADR, diagram, runbook)
├── PRD.md
└── Makefile
```

## 14. Roadmap & Milestone

| Fase | Durasi | Deliverable | Kriteria Selesai |
| --- | --- | --- | --- |
| **0. Setup** | 3–5 hari | Repo, Docker Compose (MySQL, Kafka, Redis), CI, config, migrasi awal | `make up` menjalankan semua infra; CI hijau |
| **1. Core Monolith** | 1–2 minggu | Auth, katalog, flash sale, checkout sinkron + UI dasar | Alur beli end-to-end jalan di satu binary |
| **2. Event-Driven** | 2 minggu | Kafka, order/inventory/payment async, outbox, idempotency, Redis stock gate | Saga lengkap jalan; tes duplikasi event lulus |
| **3. Reliability** | 1 minggu | Retry, DLT, timeout order, pelepasan stok, retry deadlock | Skenario kegagalan (kill consumer, restart broker) lulus |
| **4. Real-time & Notifikasi** | 1 minggu | SSE, email, notifikasi in-app | Status berubah di UI tanpa refresh |
| **5. Admin & Observability** | 1 minggu | Dashboard, metrics, tracing, logging | Dashboard Grafana menampilkan consumer lag & latensi |
| **6. Hardening** | 1 minggu | Load test k6, tuning, security review, dokumentasi | Semua metrik keberhasilan (bagian 15) tercapai |

Estimasi total: ± 8–10 minggu (kerja paruh waktu, 1 developer).

## 15. Metrik Keberhasilan

- **0 overselling** pada load test 10.000 order untuk 1.000 stok; invariant `available + reserved + sold = total_quota` terpenuhi di akhir tes.
- `POST /orders` p95 < 300 ms pada 1.000 RPS.
- Consumer lag Kafka < 5 detik saat puncak.
- ≥ 99% order mencapai status akhir (`PAID`/`FAILED`/`CANCELLED`) tanpa intervensi manual.
- 0 order ganda pada uji klik ganda / replay event.
- Coverage logic inti ≥ 70%.

## 16. Strategi Pengujian

| Level | Cakupan |
| --- | --- |
| Unit | Validasi order, state machine, perhitungan harga, aturan batas beli |
| Integration | Repository MySQL, outbox publisher, consumer idempoten (testcontainers MySQL + Kafka) |
| Concurrency | 1.000+ goroutine memesan stok terbatas; hasil tidak boleh oversell |
| Chaos sederhana | Matikan consumer di tengah proses, restart Kafka/MySQL, kirim event duplikat dan tidak berurutan |
| Load | `k6`: ramp-up ke 1.000 RPS, skenario flash sale 10.000 user untuk 1.000 stok |
| Security | Cek akses lintas user (IDOR), brute-force login, input injection |

## 17. Risiko & Mitigasi

| Risiko | Dampak | Mitigasi |
| --- | --- | --- |
| Event diproses ganda | Stok/order tidak konsisten | Idempotent consumer (`processed_events`) dalam transaksi bisnis |
| Event hilang saat crash | Order menggantung | Transactional outbox + publisher retry |
| **Hot row** stok pada MySQL | Throughput turun, lock wait | Redis stock gate; partition key `flash_sale_id`; opsi sharding stok (N baris per campaign) |
| Deadlock / lock wait MySQL | Transaksi gagal | Urutan akses tabel konsisten; transaksi singkat; retry 1213/1205 |
| Redis dan MySQL tidak sinkron | Stok tampak salah | MySQL sumber kebenaran; rekonsiliasi dan reload Redis; kembalikan counter saat gagal/cancel |
| Pembayaran sukses tapi order sudah dibatalkan (race) | Uang terambil tanpa barang | Update bersyarat + `payment.refund_required` |
| Poison message | Consumer macet | Retry terbatas + DLT + alert |
| Kompleksitas microservices | Waktu pengembangan membengkak | Modular monolith dulu, pecah bertahap |
| Lonjakan trafik | Gateway overload | Rate limiting, caching, autoscaling; opsi waiting room |
| Urutan event tidak terjamin lintas topic | Status tidak valid | State machine bersyarat; handler toleran terhadap event terlambat |

## 18. Keputusan Desain (Menjawab Pertanyaan Terbuka PRD Lama)

| Pertanyaan Lama | Keputusan |
| --- | --- |
| Monolith atau microservices? | **Mulai modular monolith (Fase 1), pecah di Fase 2–3.** Kontrak event tetap sama. |
| PostgreSQL atau MySQL? | **MySQL 8.4 (InnoDB).** |
| Frontend? | **Server-rendered (html/template + HTMX)** agar fokus ke backend. |
| Target deployment? | **Docker Compose di VPS** untuk v1; Kubernetes dan HPA masuk Future Work. |
| Format event? | **JSON** v1; Schema Registry (Avro/Protobuf) di Future Work. |
| Order multi-item? | **Tidak di v1**; satu produk per order. |

Pertanyaan yang masih terbuka:

1. Perlukah email sungguhan (SMTP provider) atau cukup Mailhog untuk demo?
2. Apakah perlu fitur *waiting room* sebelum Fase 6?
3. Target hosting demo publik (domain, TLS)?

## 19. Pengembangan Lanjutan (Future Work)

- Integrasi payment gateway nyata (Midtrans/Xendit) dan webhook.
- Antrean virtual (*waiting room*) saat flash sale.
- Kafka Schema Registry (Avro/Protobuf).
- CDC outbox dengan Debezium.
- CQRS + Elasticsearch untuk pencarian.
- Keranjang multi-item dan produk reguler.
- Deploy ke Kubernetes dengan HPA.
- Voucher dan loyalty point.
- MySQL read replica untuk dashboard admin.
