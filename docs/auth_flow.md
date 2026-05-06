# Auth Flow

Dokumen ini menjelaskan alur `authentication` dan `transaction authorization` untuk `client -> DANTE -> legacy`.

Tujuannya adalah supaya:

- DANTE tetap menjadi `client-facing middleware`
- legacy tetap menjadi sumber kebenaran untuk `credentials`, `account status`, dan approval transaksi
- pembagian tanggung jawab antara DANTE dan legacy jelas

## Core Principles

- `username + password` dipakai untuk `login`
- `access token` / `session token` dipakai setelah login berhasil
- `transaction PIN` / `mPIN` dipakai hanya untuk otorisasi transaksi finansial
- token yang dipakai client sebaiknya dibuat oleh DANTE
- legacy tetap menjadi pihak yang memvalidasi credential, status account, dan approval transaksi
- kalau legacy punya session reference atau token internal, itu cukup dipakai di jalur `DANTE <-> legacy`

## Responsibility Boundary

### DANTE responsibilities

- menerima dan memvalidasi `client request`
- menjalankan `rate limiting`, tracing, dan `audit logging`
- membuat dan memvalidasi `session/access token` untuk client
- merapikan response sebelum dikirim ke client
- menyembunyikan detail internal legacy dari client
- mengatur `async transaction flow` lewat `queue/worker components`

### Legacy responsibilities

- validate `username + password`
- validate `transaction PIN`
- mengembalikan `account status` yang authoritative
- mengembalikan `account permissions` yang authoritative
- mengeksekusi `financial transaction` seperti transfer dan QRIS payment

## Login Flow

1. Client sends `username` and `password` to DANTE.
2. DANTE memvalidasi format request, lalu menjalankan `rate limiting`, tracing, dan `audit logging`.
3. DANTE meneruskan request validasi credential ke legacy.
4. Legacy validates:
   - username exists
   - password is correct
   - `account` atau `customer` masih active
   - `channel` diizinkan
   - account tidak blocked atau restricted
5. Legacy mengembalikan hasil login yang authoritative ke DANTE.
6. DANTE membuat `client-facing session` atau `access token` miliknya sendiri.
7. DANTE mengembalikan `DANTE-issued token` ke client.

## Post-Login Flow

Setelah login berhasil, client memanggil DANTE menggunakan `DANTE-issued token`.

- `non-financial actions` cukup memakai session yang sudah authenticated
- `financial actions` harus meminta `transaction PIN` selain session yang sudah authenticated

## Example Authenticated Request

Contoh request dari client ke DANTE setelah login berhasil:

```http
GET /v1/accounts/ACC987654/balance HTTP/1.1
Host: api.dante.local
Authorization: Bearer dante-access-token-123
X-Request-ID: req-001
```

Contoh ini menunjukkan bahwa:

- client memakai token dari DANTE, bukan token raw dari legacy
- token dikirim lewat header `Authorization: Bearer ...`
- DANTE yang akan memvalidasi token tersebut sebelum meneruskan flow ke service internal atau ke legacy jika diperlukan

## Financial Transaction Flow

Untuk transfer dan QRIS payment:

1. Client sends the transaction request to DANTE with:
   - DANTE session/access token
   - transaction details
   - `transaction PIN`
2. DANTE memvalidasi session, schema, kebutuhan `idempotency`, dan `request metadata`.
3. DANTE meneruskan request transaksi ke flow eksekusi yang terhubung ke legacy.
4. Legacy validates:
   - account diizinkan untuk melakukan transaksi
   - `balance` / `limits` / `status` valid
   - `transaction PIN` benar
   - aturan transaksi lolos
5. Legacy mengeksekusi transaksi atau mengembalikan hasil gagal yang authoritative.
6. DANTE menyimpan hasilnya dan mengembalikan response sesuai `API flow`.

## Recommended Legacy Endpoints

Sisi legacy sebaiknya punya minimal business endpoints berikut:

- `POST /legacy/auth/login`
- `POST /legacy/auth/logout`
- `GET /legacy/accounts/{accountId}`
- `GET /legacy/accounts/{accountId}/balance`
- `POST /legacy/transfers`
- `POST /legacy/payments/qris`

## PIN Verification Best Practice

`transaction PIN` sebaiknya diverifikasi langsung di dalam `financial transaction endpoint`, bukan lewat `separate pre-check endpoint`.

Recommended pattern:

- `POST /legacy/transfers`
- `POST /legacy/payments/qris`

Alasannya:

- lebih sedikit `network round trip`
- `audit trail` per transaksi lebih jelas
- proses authorization dan execution tetap menyatu
- risiko state verifikasi PIN yang terpisah jadi lebih kecil

Endpoint eksplisit seperti `POST /legacy/auth/verify-transaction-pin` boleh ada kalau arsitekturnya memang butuh, tapi sebaiknya bukan default untuk project ini.

## Example Login Response From Legacy

```json
{
  "authenticated": true,
  "customer_id": "CIF123456",
  "account_id": "ACC987654",
  "account_status": "ACTIVE",
  "customer_name": "Budi Santoso",
  "permissions": {
    "can_view_balance": true,
    "can_transfer": true,
    "can_pay_qris": true
  },
  "legacy_session_reference": "LEG-SESSION-001",
  "expires_at": "2026-05-06T10:30:00Z"
}
```

## Notes

- Client sebaiknya tidak menerima raw token dari legacy, kecuali memang ada keputusan arsitektur yang sengaja mengizinkan itu.
- Semua `legacy session identifier` sebaiknya diperlakukan sebagai `internal service data` oleh DANTE.
- Error seperti `invalid credentials`, blocked account, dan legacy unavailability sebaiknya dibedakan dengan jelas supaya diagnosis operasional lebih mudah.
