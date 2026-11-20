# Cara menguji backend

## Unit, build, dan integrasi

```powershell
go test ./...
go vet ./...
go build ./...
```

Unit test mencakup range metrik, nilai non-finite, hash kanonik, dan batas operator hysteresis. Integration suite memakai `httptest`, PostgreSQL dengan ekstensi TimescaleDB, dan Redis nyata seperti pada Compose. Test mendaftarkan perangkat dan sensor, memeriksa kredensial serta rotasi, melatih validasi kepemilikan, menguji idempotensi antar-goroutine, memaksa rollback, menguji retry/ack dan dead letter, serta memeriksa alert, agregasi UTC, dan monitoring. Suite memakai schema PostgreSQL dan stream Redis unik per test. Jalankan hanya terhadap infrastruktur pengujian.

Set `TEST_DATABASE_URL` dan `TEST_REDIS_URL` dari stack lokal yang sudah berjalan. Untuk default Compose:

```powershell
$dbPassword = (Get-Content .env | Where-Object { $_ -like 'POSTGRES_PASSWORD=*' } |
    Select-Object -First 1) -replace '^POSTGRES_PASSWORD=', ''
$env:TEST_DATABASE_URL = "postgres://iot_user:${dbPassword}@127.0.0.1:5433/iot_monitor?sslmode=disable"
$env:TEST_REDIS_URL = 'redis://127.0.0.1:6379/0'
$env:GIN_MODE = 'release'
go test -count=1 -v ./...
```

Jika kedua URL tidak diatur, `go test` melompati integration suite. Jika salah satu saja disetel, suite gagal dengan pesan konfigurasi. Untuk race detector di Windows, toolchain butuh CGO dan compiler C yang sesuai. Target Docker `test` menyediakan compiler Linux untuk menjalankan langkah yang sama bersama kedua dependency. Ganti nama network jika memakai `docker compose -p` dengan nama lain.

```powershell
docker build --target test -t iot-monitoring-race:local .
$env:TEST_DATABASE_URL = "postgres://iot_user:${dbPassword}@postgres:5432/iot_monitor?sslmode=disable"
$env:TEST_REDIS_URL = 'redis://redis:6379/0'
docker run --rm --network iot-environment-monitoring_default -e TEST_DATABASE_URL -e TEST_REDIS_URL `
    -e GIN_MODE=release iot-monitoring-race:local
```

## k6

Jalankan stack terisolasi untuk benchmark. Runner mendaftarkan satu device dan satu sensor per skenario. Setiap VU mengirim event unik dan menyisipkan replay identik setiap sepuluh iterasi. Seluruh VU berbagi device tersebut, sehingga skenario ini mengukur tekanan pada satu perangkat dan satu baris status.

```powershell
'100','500','1000','2000' | ForEach-Object {
    .\scripts\load-test.ps1 -VUs $_ -Duration 30s
}
```

Runner menyimpan ringkasan k6, hasil rekonsiliasi, log, sampel `XLEN`, dan sampel CPU/memori Docker di `artifacts/load/<run>`. Ia menunggu stream kosong lalu membandingkan event unik yang menerima `202` dengan jumlah event dan reading di PostgreSQL. Ia memeriksa bahwa jumlah grup reading duplikat adalah nol. Jika jumlah berbeda, periksa timeout HTTP yang ambigu dan backlog sebelum menafsirkan hasil. Sampel backlog bisa melewatkan puncak di antara pengukuran. Hasil dipengaruhi durasi, host, konfigurasi Redis/PostgreSQL, jumlah worker, dan pola VU tertutup. [Grafana k6 Executors](https://grafana.com/docs/k6/latest/using-k6/scenarios/executors/).

Gunakan `-Rate` untuk jadwal kedatangan tetap. Angka ini menyatakan event baru per detik. Replay setiap sepuluh event menambah sekitar 10% request HTTP. `-VUs` menjadi jumlah VU yang dialokasikan untuk menjalankan jadwal itu. Runner mencatat throughput `202`, replay dengan dua respons `202`, HTTP `503`, kegagalan lain, dan iterasi k6 yang terlewat.

```powershell
.\scripts\load-test.ps1 -VUs 1000 -Rate 3000 -Duration 60s
```

Untuk perbandingan konfigurasi, jaga `WORKER_COUNT`, `QUEUE_LIMIT`, durasi, dan perangkat uji tetap setara. Default `WORKER_BATCH_SIZE` adalah 32. Atur ke 1 jika ingin mengisolasi pengaruh ukuran batch pada implementasi saat ini.

## Fleet multi-perangkat dan pemulihan worker

Jalankan dari PowerShell 7 pada proyek Compose khusus pengujian dengan stream kosong. Tetapkan `ADMIN_TOKEN` dan `POSTGRES_PASSWORD` melalui environment atau `.env`, serta gunakan port host yang tersedia. Contoh berikut menggunakan port API 18082 dan mengambil port PostgreSQL serta Redis dari konfigurasi Compose.

```powershell
$env:API_PORT = '18082'
docker compose -p iot-fleet-test up --build --wait
.\scripts\fleet-load-test.ps1 -Project iot-fleet-test
.\scripts\fleet-worker-recovery.ps1 -Project iot-fleet-test -BaseUrl http://127.0.0.1:18082
docker compose -p iot-fleet-test stop
```

Runner load mendaftarkan satu device untuk setiap VU melalui API, masing-masing dengan sensor CO₂, temperatur, dan kelembapan serta satu alert rule per sensor. Registrasi berlangsung sebelum run yang diukur. Tiga skenario bawaan berjalan 60 detik dengan 100, 500, dan 1.000 perangkat, interval per perangkat 0,8–1,2 detik, dan replay identik setiap sepuluh event. Ledger berisi satu baris untuk setiap event yang mendapat `202`. PostgreSQL mencocokkan UUID, timestamp, nilai, metrik, dan status alert dengan ledger dan profil perangkat.

Hasil mentah di `artifacts/fleet-load` mencakup sampel metrik k6 dalam CSV terkompresi, ringkasan k6, ledger, manifest perangkat publik, dan sampel Redis. API key sementara hanya dipakai di container k6. Script recovery mengambil lock PostgreSQL sementara di database uji, menghentikan worker dengan `SIGKILL` saat pesan pending, lalu menunggu consumer baru mengambil ulang ID yang sama sebelum melepas lock. Hasil dan pengamatan Redis masuk ke `artifacts/fleet-recovery`.

## PostgreSQL unavailable dan Redis restart

Jalankan setelah skenario fleet pada stack Compose pengujian dengan stream kosong. Script menghentikan satu dependency per skenario. API dan worker dibiarkan berjalan untuk menguji reconnect. Perangkat uji mengirim event dan replay sebelum outage, event baru saat outage, lalu event pemulihan setelah dependency siap. Script memverifikasi status HTTP, pemulihan pending list, dan isi PostgreSQL.

```powershell
.\scripts\dependency-recovery-test.ps1 -Project iot-fleet-test -BaseUrl http://127.0.0.1:18082
```

Kedua skenario memeriksa `503` dan `Retry-After: 2` pada ingestion dan readiness saat dependency berhenti. Skenario PostgreSQL juga memeriksa respons tersebut pada heartbeat yang memakai API key dan pada pembacaan perangkat oleh admin. Hasil disimpan di `artifacts/dependency-recovery`.

Skenario Redis memakai shutdown terkontrol dengan AOF `everysec` dan volume yang sama. Kegagalan host yang mendadak tetap dapat menghilangkan penulisan yang belum tersinkron ke disk. [Redis Persistence](https://redis.io/docs/latest/operate/oss_and_stack/management/persistence/)

