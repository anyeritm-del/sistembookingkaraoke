# Sistem Booking Room Karaoke

Booking room karaoke untuk staf/kasir, berjalan di **Vercel** (Go serverless),
data disimpan di **Google Sheets**, dan **timer + alarm di TV** setiap room.

| Bagian | Lokasi | Keterangan |
|---|---|---|
| Halaman staf | `/` ([public/index.html](public/index.html)) | Login username + PIN, jadwal per room, booking, check-in, perpanjang, check-out, batal, laporan, aktivitas, kelola room & user |
| Halaman TV | `/tv?room=R01&key=...` ([public/tv.html](public/tv.html)) | Sisa waktu, peringatan 5 menit, alarm saat waktu habis |
| API | `/api/*` ([api/index.go](api/index.go) → [pkg/httpapi](pkg/httpapi)) | Satu Go function di Vercel |
| Aturan bisnis | [pkg/booking](pkg/booking) | Harga, cek bentrok, status, laporan |
| Penyimpanan | [pkg/sheetstore](pkg/sheetstore) | Google Sheets API |
| Aplikasi Android TV | [android-tv/](android-tv) | APK WebView fullscreen, auto-start saat TV nyala |

## Role dan hak akses

| Aksi | Staff | Supervisor | Admin |
|---|:-:|:-:|:-:|
| Lihat jadwal & daftar, booking baru, check-in, perpanjang, check-out | ✓ | ✓ | ✓ |
| Konfirmasi Tentative, batalkan Tentative | ✓ | ✓ | ✓ |
| Batalkan booking Confirm | | ✓ | ✓ |
| Laporan harian, log aktivitas | | ✓ | ✓ |
| Kelola room (tambah, ubah nama, aktif/nonaktif) dan tabel harga | | | ✓ |
| Kelola user (tambah, ubah role, nonaktifkan, reset PIN) | | | ✓ |
| Kelola TV (buat kode pairing, cabut TV) | | | ✓ |

- Tabel hak akses ada di satu tempat: [pkg/booking/roles.go](pkg/booking/roles.go).
  Server memeriksanya di setiap request; halaman web hanya menyembunyikan tombol.
- Role user dibaca ulang dari sheet di setiap request (cache 5 detik), jadi
  menonaktifkan user atau menurunkan role langsung berlaku.
- Semua user bisa mengganti PIN sendiri (tombol **Ganti PIN**). PIN 6-12 angka;
  angka sama (`111111`) atau berurutan (`123456`) ditolak.
- Admin tidak bisa menurunkan role atau menonaktifkan akun sendiri, dan harus
  selalu ada minimal satu admin aktif.
- Salah PIN 5 kali: username dikunci 5 menit.
- **Login pertama**: selama tab `Users` kosong, login dengan username `admin`
  dan PIN = `ADMIN_PIN`. Akun admin itu langsung tersimpan di sheet; ganti PIN-nya
  setelah login. Jika semua admin terkunci, kosongkan isi tab `Users` (sisakan
  header) untuk memakai `ADMIN_PIN` lagi.

**Log aktivitas** (tab `Activity`): login, ganti/reset PIN, booking baru,
check-in, perpanjang, check-out, batal, perubahan room dan user, lengkap
dengan waktu, username, dan detail (mis. `tarif Rp100.000 -> Rp120.000`).
Booking juga menyimpan `created_by`, `confirmed_by`, `checked_in_by`, `checked_out_by`, `cancelled_by`, dan `hold_until` (batas tahan Tentative).

## Alur booking

```
tentative ──konfirmasi──> booked (Confirm) ──check-in──> checked_in ──check-out──> finished
    │                         │                              └─ +30 / +1 jam (perpanjang)
    └──batal──> cancelled <───┘ batal
```

- **Confirm** (`booked` di sheet): booking pasti.
- **Tentative**: belum pasti, menahan slot sampai `hold_until` = 2 jam sebelum
  jam mulai (minimal 30 menit dari saat dibuat, tidak lewat jam mulai). Setelah
  itu tampil **Kedaluwarsa** dan slot terbuka untuk tamu lain. Booking
  kedaluwarsa masih bisa dikonfirmasi jika slotnya masih kosong. Tentative harus
  dikonfirmasi dulu sebelum check-in, dan tidak bisa diperpanjang.
- **Tab Daftar**: semua booking dengan filter status (Confirm, Tentative,
  Tentative kedaluwarsa, Cancel, Check-in, Selesai), rentang tanggal mulai
  (maks. 92 hari, default hari ini + 30 hari), dan pencarian nama/HP/catatan.
  Menampilkan jumlah dan total harga, serta tombol aksi di tiap baris.

- **Harga** (menu **Harga**, tab `Pricing`), sama untuk semua room:

  | Hari | Mulai 11:00 – 16:59 | Mulai 17:00 – 10:59 |
  |---|---|---|
  | Senin – Jumat | Rp 60.000 / jam | Rp 120.000 / jam |
  | Sabtu – Minggu | Rp 85.000 / jam | Rp 170.000 / jam |

  Harga per jam ditentukan oleh **jam mulai**; seluruh durasi dan perpanjangan
  memakai harga itu (16:00–18:00 hari kerja = 2 × 60.000). Hari dihitung dari
  tanggal kalender jam mulai (Sabtu 00:30 = weekend). Durasi kelipatan 30 menit,
  maks 12 jam. Harga dikunci saat booking dibuat, jadi perubahan harga tidak
  mengubah booking lama. Jika tab `Pricing` dikosongkan, tarif per room di tab
  `Rooms` dipakai lagi.
- Booking tidak boleh bentrok dengan booking aktif lain di room yang sama.
  Perpanjang juga dicek bentrok dengan booking berikutnya.
- Check-in paling cepat 60 menit sebelum jam mulai, dan hanya jika room tidak
  sedang dipakai tamu lain yang belum check-out.
- Laporan harian menghitung booking berdasarkan tanggal mulai. Pendapatan =
  booking `finished` + `checked_in`. Confirm yang belum check-in dan Tentative
  dihitung terpisah, tidak masuk pendapatan.

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

**Pengingat di halaman staf/kasir** (semua role, di tab mana pun): halaman
staf memantau semua room yang sedang check-in hari ini (tiap 15 detik).

- Sisa ≤ 5 menit: kartu oranye di pojok kanan bawah dengan hitung mundur,
  bunyi peringatan sekali, dan tombol **+30 mnt / +1 jam / Check-out / Tutup**.
- Waktu habis: kartu merah berkedip dan sirene tiap 2,5 detik sampai staf
  check-out, perpanjang, atau menekan **Senyapkan**.
- Judul tab browser berkedip `⏰ (2) Hampir habis` agar terlihat dari jendela lain.
- Browser hanya mengizinkan suara setelah ada klik. Setelah login suara langsung
  aktif; jika halaman dibuka ulang, tekan **🔔 Aktifkan suara alarm** atau klik di mana saja.
- Biarkan tab halaman staf tetap terbuka. Browser bisa memperlambat timer di tab
  yang lama tidak dilihat, sehingga bunyi bisa tertunda hingga ±1 menit; kartu
  tetap tepat saat tab dibuka.

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

- **Rooms**: `id | name | rate_per_hour | active` — sebaiknya diubah lewat
  menu **Room** (admin) supaya tercatat di log; edit langsung di sheet tetap bisa.
- **Bookings**: `id | room_id | customer_name | phone | start | end | duration_minutes | status | rate_per_hour | total_price | notes | checked_in_at | checked_out_at | created_at | updated_at | created_by | checked_in_by | checked_out_by | cancelled_by | confirmed_by | hold_until`
  — diisi oleh aplikasi. Waktu dalam WIB, format `YYYY-MM-DD HH:MM`.
  Boleh menambah kolom sendiri di kanan; isinya tidak akan ditimpa.
- **Users**: `username | name | role | pin_hash | active | created_at | updated_at`
  — kelola lewat menu **User**. `pin_hash` tidak bisa dibalik menjadi PIN tanpa
  `PIN_PEPPER` yang hanya ada di server. Batasi siapa yang bisa membuka spreadsheet.
- **Activity**: `time | username | action | booking_id | room_id | detail` — hanya ditambah, jangan diedit.
- **Pricing**: `day_type | start | end | rate_per_hour` — `weekday`/`weekend`,
  jam `HH:MM`; jam selesai lebih kecil dari jam mulai = melewati tengah malam.
  Ubah lewat menu **Harga** (admin), yang memeriksa setiap jenis hari menutup
  24 jam tanpa tumpang tindih.
- **Devices**: `id | name | room_id | status | token_hash | pair_code_hash | pair_expires | created_by | created_at | paired_at | revoked_by`
  — TV yang dipasangkan; kelola lewat menu **TV**, jangan diedit.

Setelah update aplikasi yang menambah kolom/tab, jalankan lagi
`go run ./cmd/sheetsetup -check` lalu `go run ./cmd/sheetsetup`. Perintah ini
hanya menambah tab dan kolom di ujung kanan; data lama tidak diubah.

> Jangan ubah nama header atau isi kolom `id`. Hindari mengedit baris booking
> yang sedang aktif langsung di sheet; pakai halaman staf.

### 2. Deploy ke Vercel

1. Push folder ini ke Git (GitHub/GitLab), lalu *Import Project* di Vercel.
   Framework preset: **Other**. Tidak perlu build command.
2. Set **Environment Variables** (lihat [.env.example](.env.example)):

   | Nama | Isi |
   |---|---|
   | `ADMIN_PIN` | PIN untuk login pertama sebagai `admin` (6-12 angka) |
   | `SESSION_SECRET` | `openssl rand -base64 48` |
   | `PIN_PEPPER` | `openssl rand -base64 48` — **jangan pernah diganti** setelah ada user |
   | `TV_KEY` | `openssl rand -hex 16` |
   | `SPREADSHEET_ID` | ID spreadsheet |
   | `GOOGLE_SERVICE_ACCOUNT_JSON` | isi file JSON, atau base64-nya: `base64 -i key.json` |
   | `APP_TIMEZONE` | opsional, default `Asia/Jakarta` |

3. Deploy. Cek `https://<app>.vercel.app/api/health` → `{"ok":true,...}`.
4. Buka `https://<app>.vercel.app/`, login dengan PIN.

### 3. TV di setiap room

**Pairing (cara utama).** Setiap TV mendapat kunci sendiri; tidak perlu mengetik
kode room atau TV key.

1. Admin buka menu **TV → + Pasang TV baru**, pilih room, beri nama TV, lalu
   **Buat kode pairing**. Muncul kode 6 angka, berlaku 15 menit, sekali pakai.
2. Di TV, buka aplikasi TV (atau halaman `/tv` di browser TV). Layar
   **Pengaturan TV** meminta kode pairing; ketik 6 angka itu dengan remote.
3. TV langsung menampilkan room-nya (nama room di pojok kiri atas).

TV yang hilang, rusak, atau dipindah ruangan: menu **TV → Cabut**. TV itu
kembali ke layar pairing; TV lain tidak terpengaruh. Kode yang belum dipakai
bisa dibatalkan dengan tombol yang sama. Semua kejadian tercatat di Aktivitas.
Server hanya menyimpan hash kode dan hash token (tab `Devices`). Salah kode 10
kali dari alamat yang sama mengunci pairing dari alamat itu selama 10 menit.

**Cara lama (masih didukung):** kode room + `TV_KEY` bersama, lewat
`/tv?room=R01&key=<TV_KEY>` atau tautan "Cara lama" di layar pengaturan.

**Opsi A – Browser TV / mini PC / Chromecast dengan browser:**
buka `https://<app>.vercel.app/tv` dalam mode fullscreen, lalu masukkan kode pairing.
Tekan OK sekali saat muncul "Tekan OK untuk mengaktifkan suara" (aturan
autoplay browser). Pengaturan tersimpan di TV; buka `/tv?setup=1` untuk mengubah.

**Opsi B – Aplikasi Android TV (disarankan untuk Android TV / TV box):**
suara alarm langsung aktif tanpa tekan tombol, layar tidak mati, dan aplikasi
terbuka sendiri setelah TV dinyalakan. Alamat server sudah terisi otomatis.

Build APK rilis (ditandatangani dengan release key; nilai `KARAOKE_TV_*` ada di `.env`):

```sh
set -a; . ./.env; set +a
cd android-tv
JAVA_HOME=/opt/homebrew/opt/openjdk@17 ./gradlew assembleRelease
# APK: android-tv/app/build/outputs/apk/release/app-release.apk
```

> **Simpan cadangan keystore** `~/.config/karaoke-tv/release.jks` beserta
> password-nya (di `.env`) di tempat aman, misalnya password manager. Android
> hanya mau memasang update yang ditandatangani dengan key yang sama; jika key
> hilang, aplikasi di setiap TV harus di-uninstall dulu sebelum versi baru dipasang.

Pasang ke TV:

1. Di TV: *Settings → Device Preferences → About*, klik **Build** 7 kali untuk
   membuka Developer options. Lalu *Developer options →* aktifkan **USB debugging**
   / **Network debugging (ADB)**. Catat IP TV (*Settings → Network*).
2. Dari komputer di jaringan yang sama:

   ```sh
   adb connect <IP-TV>:5555          # setujui prompt "Allow debugging" di TV
   adb install -r android-tv/app/build/outputs/apk/release/app-release.apk
   # Izinkan auto-start setelah TV dinyalakan (Android 10+):
   adb shell appops set com.sentineltech.karaoketv SYSTEM_ALERT_WINDOW allow
   adb shell monkey -p com.sentineltech.karaoketv 1   # buka aplikasinya
   ```

3. Aplikasi langsung membuka layar pairing (alamat server sudah terisi).
   Ketik kode 6 angka dari menu **TV** di halaman admin.

   Cara lama: tekan lama BACK untuk membuka pengaturan aplikasi, isi kode room
   dan TV key (`adb shell input text "$TV_KEY"` untuk mengetik key dari komputer).

Tanpa adb (mis. TV tanpa Developer options): salin APK ke flashdisk, buka dengan
aplikasi file manager di TV, izinkan *Install unknown apps*, lalu masukkan
kode pairing dengan remote. Auto-start setelah boot butuh langkah `appops` di atas.

Buka pengaturan lagi: **tekan lama BACK** atau tekan **MENU**. Tombol BACK
biasa diabaikan supaya tamu tidak keluar dari timer.

Update aplikasi: naikkan `versionCode` di `android-tv/app/build.gradle.kts`,
build ulang, lalu `adb install -r` lagi (pengaturan di TV tetap tersimpan).

---

## Pengembangan lokal

```sh
# Tanpa Google Sheets (data di memori, hilang saat server berhenti):
STORE=memory ADMIN_PIN=112233 \
SESSION_SECRET=dev-secret-dev-secret-dev-secret-123 \
PIN_PEPPER=dev-pepper-dev-pepper-dev-pepper-12 TV_KEY=dev-tv-key-123456 \
go run ./cmd/devserver
# Login: username admin, PIN 112233
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
- **Kunci login per instance**: hitungan salah PIN disimpan di memori server,
  jadi berlaku per instance Vercel. Setiap gagal login juga diperlambat 1 detik.
- **Sheet terus bertambah**: arsipkan baris `Bookings` lama (misal per tahun)
  ke spreadsheet lain agar baca tetap cepat.
- Rollback: Vercel menyimpan semua deployment; pakai *Instant Rollback* di
  dashboard. Data di Sheets punya *Version history* (File → Version history).
