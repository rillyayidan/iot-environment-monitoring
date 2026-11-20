# Dashboard demo IoT

Dashboard lokal untuk mendemonstrasikan heartbeat, ingestion asinkron, deduplikasi, alert, histori, dan agregasi melalui API yang ada.

## Jalankan

Dari root repo, jalankan Docker Compose dengan konfigurasi `.env` proyek.

```powershell
docker compose up --build --wait
go run ./demo-ui
```

Buka [dashboard lokal](http://127.0.0.1:8090). Jika port backend diubah lewat `API_PORT`, jalankan `go run ./demo-ui -api-port <port>` dengan angka port yang sesuai. Port dashboard dapat disesuaikan dengan flag `-port`.

## Demo

1. Klik **Hubungkan token** dan masukkan `ADMIN_TOKEN` dari `.env`.
2. Klik **Siapkan perangkat demo**. Dashboard mendaftarkan satu perangkat, tiga sensor, dan satu aturan alert per sensor.
3. Klik **Kirim heartbeat** dan perhatikan status berubah dari `NEVER_SEEN` menjadi `ONLINE`.
4. Klik **Kirim pembacaan tinggi**. Badge `HTTP 202` menunjukkan Redis menerima event. Histori dan tiga alert `ACTIVE` muncul setelah worker memproses event.
5. Klik **Ulangi event terakhir**. Dashboard mengirim kembali payload dan `event_id` yang sama. Setelah antrean selesai, histori berisi satu pembacaan per sensor untuk event tersebut.
6. Klik **Kirim pembacaan pemulihan**. Tiga alert yang sama berubah menjadi `RESOLVED`.
7. Di **Histori & agregasi**, pilih **Per menit** atau **Per jam** untuk melihat rata-rata, minimum, maksimum, dan jumlah sampel.

Profil demo memakai batas atas ruang kerja dari Tabel 6 lampiran Permenkes No. 48 Tahun 2016. Ambang pulih dipilih untuk hysteresis. [Lampiran Permenkes](https://peraturan.bpk.go.id/Home/Download/104043/Permenkes%20Nomor%2048%20Tahun%202016.pdf)

| Sensor | Alert aktif | Alert pulih | Reading tinggi | Reading pemulihan |
| --- | ---: | ---: | ---: | ---: |
| CO₂ | > 1.000 ppm | ≤ 900 ppm | 1.200 ppm | 850 ppm |
| Suhu | > 26 °C | ≤ 25 °C | 31,5 °C | 25 °C |
| Kelembapan | > 60% RH | ≤ 55% RH | 74% RH | 55% RH |

Preset membuat aturan saat **Siapkan perangkat demo** dijalankan. Untuk memakai preset terbaru, mulai sesi dashboard dan buat perangkat demo baru.

## Batas demo

Server dashboard mendengarkan di loopback dan meneruskan request ke backend lokal. Token operator dan API key perangkat disimpan di memori halaman. Refresh halaman mengakhiri sesi dashboard. Perangkat, sensor, dan reading yang sudah dibuat tetap tersimpan di PostgreSQL.

Untuk melepas dashboard, hentikan proses `go run` dengan Ctrl+C dan hapus folder `demo-ui`.
