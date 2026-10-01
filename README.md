# Sistem Booking Room Karaoke

Booking room karaoke untuk staf/kasir, berjalan di **Vercel** (Go serverless),
data disimpan di **Google Sheets**, dan **timer + alarm di TV** setiap room.

| Bagian | Lokasi | Keterangan |
|---|---|---|
| Halaman staf | `/` ([public/index.html](public/index.html)) | Login PIN, jadwal per room, booking baru, check-in, perpanjang, check-out, batal, laporan harian |
| Halaman TV | `/tv?room=R01&key=...` ([public/tv.html](public/tv.html)) | Sisa waktu, peringatan 5 menit, alarm saat waktu habis |
| API | `/api/*` ([api/index.go](api/index.go) → [pkg/httpapi](pkg/httpapi)) | Satu Go function di Vercel |
| Aturan bisnis | [pkg/booking](pkg/booking) | Harga, cek bentrok, status, laporan |
| Penyimpanan | [pkg/sheetstore](pkg/sheetstore) | Google Sheets API |
| Aplikasi Android TV | [android-tv/](android-tv) | APK WebView fullscreen, auto-start saat TV nyala |

## Alur booking

```
booked ──check-in──> checked_in ──check-out──> finished
   └──batal──> cancelled          └─ +30 / +1 jam (perpanjang)
```

- Harga = tarif per jam room × durasi (kelipatan 30 menit, maks 12 jam). Tarif
  dikunci saat booking dibuat, jadi perubahan tarif tidak mengubah booking lama.
- Booking tidak boleh bentrok dengan booking aktif lain di room yang sama.
  Perpanjang juga dicek bentrok dengan booking berikutnya.
- Check-in paling cepat 60 menit sebelum jam mulai, dan hanya jika room tidak
  sedang dipakai tamu lain yang belum check-out.
- Laporan harian menghitung booking berdasarkan tanggal mulai. Pendapatan =
  booking `finished` + `checked_in`.

## Alarm TV

| Kondisi | Tampilan | Suara |
|---|---|---|
| Tamu sudah check-in | Nama tamu, hitung mundur besar, progress bar | – |
| Sisa ≤ 5 menit | Angka dan banner oranye "Waktu tinggal N menit" | Chime 2× (sekali per jam selesai) |
| Waktu habis | Layar merah berkedip "WAKTU HABIS" | Sirene berulang tiap 2,5 detik |

Alarm waktu habis **berbunyi terus sampai staf check-out atau perpanjang** di
halaman staf. Tombol "Senyapkan 2 menit" di TV hanya menunda; setelah 2 menit
alarm bunyi lagi. Setelah perpanjang, peringatan 5 menit akan muncul lagi
untuk jam selesai yang baru.

TV mengambil status dari server tiap 20 detik (10 detik saat peringatan/alarm),
dan menghitung mundur sendiri tiap detik memakai jam server. Jika internet
putus, timer tetap berjalan dan alarm tetap bunyi.

---

## Setup (sekali saja)

### 1. Google Sheets + Service Account

1. Buat spreadsheet kosong di Google Sheets. Salin ID dari URL:
   `https://docs.google.com/spreadsheets/d/<SPREADSHEET_ID>/edit`
2. Di [Google Cloud Console](https://console.cloud.google.com/):
   - Buat project (atau pakai yang ada), aktifkan **Google Sheets API**.
   - *IAM & Admin → Service Accounts → Create*. Tidak perlu role.
   - Di service account: *Keys → Add key → JSON*. Simpan file-nya dengan aman.
3. **Share** spreadsheet ke email service account (`...@...iam.gserviceaccount.com`)
   sebagai **Editor**.
4. Buat tab dan header otomatis (opsi `-seed` menambah 4 contoh room):

   ```sh
   # isi .env (lihat .env.example), GOOGLE_SERVICE_ACCOUNT_FILE=/path/ke/key.json
   set -a; . ./.env; set +a
   go run ./cmd/sheetsetup -check   # hanya membaca: daftar tab
   go run ./cmd/sheetsetup -seed    # buat tab + 4 contoh room
   ```

   Perintah ini hanya menambah tab yang belum ada; data yang sudah ada tidak diubah.

Struktur sheet (baris 1 = header, kolom dicari berdasarkan nama header):

- **Rooms**: `id | name | rate_per_hour | active` — edit langsung di sheet
  untuk menambah room atau mengubah tarif. `active` = TRUE/FALSE.
- **Bookings**: `id | room_id | customer_name | phone | start | end | duration_minutes | status | rate_per_hour | total_price | notes | checked_in_at | checked_out_at | created_at | updated_at`
  — diisi oleh aplikasi. Waktu dalam WIB, format `YYYY-MM-DD HH:MM`.
  Boleh menambah kolom sendiri di kanan; isinya tidak akan ditimpa.

> Jangan ubah nama header atau isi kolom `id`. Hindari mengedit baris booking
> yang sedang aktif langsung di sheet; pakai halaman staf.

### 2. Deploy ke Vercel

1. Push folder ini ke Git (GitHub/GitLab), lalu *Import Project* di Vercel.
   Framework preset: **Other**. Tidak perlu build command.
2. Set **Environment Variables** (lihat [.env.example](.env.example)):

   | Nama | Isi |
   |---|---|
   | `ADMIN_PIN` | PIN staf, minimal 6 digit disarankan |
   | `SESSION_SECRET` | `openssl rand -base64 48` |
   | `TV_KEY` | `openssl rand -hex 16` |
   | `SPREADSHEET_ID` | ID spreadsheet |
   | `GOOGLE_SERVICE_ACCOUNT_JSON` | isi file JSON, atau base64-nya: `base64 -i key.json` |
   | `APP_TIMEZONE` | opsional, default `Asia/Jakarta` |

3. Deploy. Cek `https://<app>.vercel.app/api/health` → `{"ok":true,...}`.
4. Buka `https://<app>.vercel.app/`, login dengan PIN.

### 3. TV di setiap room

**Opsi A – Browser TV / mini PC / Chromecast dengan browser:**
buka `https://<app>.vercel.app/tv?room=R01&key=<TV_KEY>` dalam mode fullscreen.
Tekan OK sekali saat muncul "Tekan OK untuk mengaktifkan suara" (aturan
autoplay browser). Pengaturan tersimpan di TV; buka `/tv?setup=1` untuk mengubah.

**Opsi B – Aplikasi Android TV (disarankan untuk Android TV / TV box):**
suara alarm langsung aktif tanpa tekan tombol, layar tidak mati, dan aplikasi
terbuka sendiri setelah TV dinyalakan.

```sh
cd android-tv
# Opsional: ganti DEFAULT_SERVER di app/build.gradle.kts dengan URL Vercel Anda
JAVA_HOME=/opt/homebrew/opt/openjdk@17 ./gradlew assembleDebug
# APK: app/build/outputs/apk/debug/app-debug.apk

# Pasang ke TV (aktifkan Developer options + USB/Network debugging di TV):
adb connect <IP-TV>:5555
adb install -r app/build/outputs/apk/debug/app-debug.apk
# Izinkan auto-start setelah boot (Android 10+):
adb shell appops set com.sentineltech.karaoketv SYSTEM_ALERT_WINDOW allow
```

Saat pertama dibuka, isi alamat server, kode room, dan TV key.
Buka pengaturan lagi: **tekan lama BACK** atau tekan **MENU**. Tombol BACK
biasa diabaikan supaya tamu tidak keluar dari timer.

Untuk APK rilis (ditandatangani), buat keystore dan tambahkan `signingConfigs`
di `app/build.gradle.kts`; simpan keystore di luar repo.

---

## Pengembangan lokal

```sh
# Tanpa Google Sheets (data di memori, hilang saat server berhenti):
STORE=memory ADMIN_PIN=123456 \
SESSION_SECRET=dev-secret-dev-secret-dev-secret-123 TV_KEY=dev-tv-key-123456 \
go run ./cmd/devserver
# Staf: http://localhost:8080   TV: http://localhost:8080/tv?room=R01&key=dev-tv-key-123456

go test ./...        # unit test aturan booking, auth, parsing sheet, API
vercel build         # cek build Vercel secara lokal (perlu `vercel link`)
```

## Batasan yang perlu diketahui

- **Kuota Google Sheets**: sekitar 60 request baca/menit per service account.
  Server membaca kedua tab dalam 1 request dan menyimpan cache 5 detik per
  instance. Dengan polling TV 20 detik, ±15 room masih aman. Jika room lebih
  banyak, naikkan `POLL_MS` di [public/js/tv.js](public/js/tv.js) atau
  pindahkan ke PostgreSQL (cukup buat implementasi `booking.Store` baru).
- **Tidak ada transaksi** di Google Sheets. Dalam satu instance, penulisan
  diantre dan cek bentrok selalu membaca data terbaru. Dua staf yang menyimpan
  booking ke room dan jam yang sama di detik yang sama masih bisa lolos
  (sangat jarang untuk tim kecil).
- **Satu PIN untuk semua staf**: tidak tercatat siapa yang melakukan aksi.
  Gagal login diperlambat 1 detik; gunakan PIN 6+ digit dan ganti berkala.
- **Sheet terus bertambah**: arsipkan baris `Bookings` lama (misal per tahun)
  ke spreadsheet lain agar baca tetap cepat.
- Rollback: Vercel menyimpan semua deployment; pakai *Instant Rollback* di
  dashboard. Data di Sheets punya *Version history* (File → Version history).
