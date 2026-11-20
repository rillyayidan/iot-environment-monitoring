## Persiapan Windows

Jalankan perintah dari root repo di **PowerShell 7** dengan Docker Desktop menyala. Pakai tiga terminal selama demo. Terminal A untuk dashboard, B untuk request manual, dan C untuk pengujian terisolasi.

### Terminal A · Nyalakan backend dan dashboard

Jika `.env` sudah ada dan stack bisa berjalan, pakai kembali file itu. Untuk clone baru, blok ini membuat dua secret acak dan menyimpan pengaturan default dari `.env.example` tanpa menampilkan nilainya.

```powershell
if (-not (Test-Path -LiteralPath '.env')) {
    $newDbPassword = [Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(32))
    $newAdminToken = [Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(32))
    $template = Get-Content -LiteralPath '.env.example' -Raw
    $template -replace '(?m)^POSTGRES_PASSWORD=[^\r\n]*', "POSTGRES_PASSWORD=$newDbPassword" `
              -replace '(?m)^ADMIN_TOKEN=[^\r\n]*', "ADMIN_TOKEN=$newAdminToken" |
        Set-Content -LiteralPath '.env' -Encoding ascii
}
```

Jika .env masih kosong, jalankan perintah berikut untuk membuat dua nilai terpisah. Isi baris pertama sebagai POSTGRES_PASSWORD dan kedua sebagai ADMIN_TOKEN, lalu jalankan Compose.
```powershell
1..2 | ForEach-Object {
    [Convert]::ToHexString([Security.Cryptography.RandomNumberGenerator]::GetBytes(32))
}
```
`POSTGRES_PASSWORD` adalah password database. `ADMIN_TOKEN` adalah token operator untuk mengelola perangkat dan membaca data API. Saat perangkat didaftarkan, backend membuat `X-API-Key` khusus untuk perangkat itu.

Supaya transisi offline bisa diperagakan dalam waktu singkat, gunakan timeout 30 detik dan interval monitoring 3 detik di terminal demo. Dua variabel ini berlaku di sesi terminal. Timeout bawaan dari `.env.example` adalah 5 menit, dan monitoring bawaan berjalan setiap 15 detik.

```powershell
$env:OFFLINE_TIMEOUT = '30s'
$env:MONITOR_INTERVAL = '3s'

docker compose up --build --wait
docker compose ps -a
Invoke-RestMethod 'http://127.0.0.1:8080/health/ready'

go run ./demo-ui
```

`up --build --wait` membangun image backend dan menunggu layanan siap. `ps -a` juga menampilkan container migrasi yang selesai dengan `Exited (0)`. `Invoke-RestMethod` memanggil HTTP dan menampilkan hasil JSON sebagai objek PowerShell. Pastikan status API `ready` sebelum membuka [dashboard lokal](http://127.0.0.1:8090/).

`go run ./demo-ui` menjalankan server dashboard di port 8090. Browser mengakses dashboard, lalu server tersebut meneruskan request API ke port 8080. Gunakan `/health/ready` untuk mengecek backend karena rute `/` pada port 8080 mengembalikan `404`. Biarkan terminal ini terbuka. Kalau muncul `bind: Only one usage...`, buka port 8090 di browser untuk memeriksa apakah dashboard sudah berjalan. Alternatifnya, jalankan `go run ./demo-ui -port 8091`, lalu buka port 8091. Tekan **Ctrl+C** di terminal dashboard untuk menghentikannya.

### Terminal B · Siapkan token HTTP dan nama perangkat

```powershell
$base = 'http://127.0.0.1:8080/api/v1'
$tokenLine = Get-Content -LiteralPath '.env' |
    Where-Object { $_ -like 'ADMIN_TOKEN=*' } | Select-Object -First 1
$adminToken = $env:ADMIN_TOKEN
if (-not $adminToken) { $adminToken = $tokenLine -replace '^ADMIN_TOKEN=', '' }
if (-not $adminToken) { throw 'ADMIN_TOKEN belum diisi.' }

$admin = @{ Authorization = "Bearer $adminToken" }
$demoName = 'Demo ' + (Get-Date -Format 'HHmmss')
$demoName
Set-Clipboard -Value $adminToken
```

`$base` menghindari mengetik ulang alamat API. `$admin` adalah kumpulan header yang dikirim melalui `-Headers`. `Set-Clipboard` menyalin token tanpa mencetaknya di terminal. Klik **Hubungkan token**, tempel token, lalu klik **Hubungkan**. Pakai nama yang dicetak `$demoName` pada UI. Pertahankan terminal B agar variabel untuk langkah berikutnya tetap tersedia.

Contoh memakai port dan akun bawaan `.env.example`. Jika kamu mengganti konfigurasi, sesuaikan port, user, nama database, dan stream di perintah. Pada demo macOS, bisa mengikuti startup di [README](../README.md) dan menjalankan `bash scripts/demo.sh` untuk skenario terminal.

## 1. REST API untuk device management dan ingestion

1. Di dashboard, isi nama yang tercetak tadi dan klik **Siapkan perangkat demo**. UI mengirim `POST /devices`, lalu membuat tiga sensor dan satu aturan alert untuk masing-masing sensor.
2. Tunjukkan **NEVER_SEEN** sekarang juga untuk requirement 6. Perangkat baru belum mengirim heartbeat atau reading.
3. Di terminal B, ambil katalog untuk menunjukkan bahwa perangkat tersimpan. Terakhir, klik **Kirim pembacaan tinggi** sekali dan tunggu histori menunjukkan tiga reading. Simpan Event ID lengkap di kartu **Event terakhir** untuk requirement 3.

```powershell
$devices = Invoke-RestMethod "$base/devices?limit=200" -Headers $admin
$demoDevice = $devices | Where-Object name -eq $demoName | Select-Object -First 1
if (-not $demoDevice) { throw 'Nama perangkat UI belum ditemukan di API.' }
$demoUrl = "$base/devices/$($demoDevice.id)"

Invoke-RestMethod $demoUrl -Headers $admin | Select-Object id,name,location,is_active
$demoSensors = Invoke-RestMethod "$demoUrl/sensors" -Headers $admin
$demoSensors | Format-Table id,metric,unit
$demoCo2 = $demoSensors | Where-Object metric -eq 'co2'
```

`GET` membaca tanpa mengubah data. `Where-Object` memilih perangkat yang baru dibuat berdasarkan namanya. `$($demoDevice.id)` memasukkan UUID publik ke URL dan `Select-Object` membatasi kolom yang ditampilkan. Kamu harus melihat `co2/ppm`, `temperature/celsius`, dan `humidity/percent`. Jika punyamu lebih dari 200 perangkat, gunakan `offset` untuk pindah halaman daftar.

Untuk menunjukkan operasi edit, `PATCH` mengubah field yang dikirim. Jalankan blok ini lalu tunjukkan lokasi baru di respons.

```powershell
$patchBody = @{ location = 'Lab Ujian' } | ConvertTo-Json -Compress
Invoke-RestMethod -Method Patch -Uri $demoUrl -Headers $admin `
    -ContentType 'application/json' -Body $patchBody |
    Select-Object id,name,location
```

**File dan fungsi.** [demo-ui/app.js](../demo-ui/app.js) `setup()` dan `sendReading()` membuat data demo dan mengirim HTTP. [internal/handler/catalog.go](../internal/handler/catalog.go) `RegisterCatalog` memasang rute perangkat/sensor, [internal/catalog/catalog.go](../internal/catalog/catalog.go) `RegisterDevice`, `RegisterSensor`, dan `PatchDevice` menangani aturan, serta [internal/catalog/postgres.go](../internal/catalog/postgres.go) `CreateDevice` dan `CreateSensor` menyimpan keduanya. [internal/handler/ingest.go](../internal/handler/ingest.go) `RegisterIngestion` mengembalikan `202` setelah `Submit` berhasil.

## 2. Autentikasi setiap perangkat dengan API Key

Satu tombol UI sudah mengirim key yang valid. Untuk membuktikan bahwa backend memeriksa key, gunakan endpoint heartbeat yang sama dengan beberapa header berbeda. Buat **perangkat uji PowerShell terpisah** supaya key perangkat dashboard tetap bisa dipakai.

```powershell
$authBody = @{ name = "$demoName CLI"; location = 'Lab demo' } | ConvertTo-Json -Compress
$authDevice = Invoke-RestMethod -Method Post -Uri "$base/devices" -Headers $admin `
    -ContentType 'application/json' -Body $authBody
$authUrl = "$base/devices/$($authDevice.id)"
$heartbeat = "$base/devices/heartbeat"
$oldKey = @{ 'X-API-Key' = $authDevice.api_key }

(Invoke-WebRequest -Method Post -Uri $heartbeat -SkipHttpErrorCheck).StatusCode
(Invoke-WebRequest -Method Post -Uri $heartbeat -Headers $oldKey -SkipHttpErrorCheck).StatusCode

$rotated = Invoke-RestMethod -Method Post -Uri "$authUrl/keys/rotate" -Headers $admin
$deviceKey = @{ 'X-API-Key' = $rotated.api_key }

(Invoke-WebRequest -Method Post -Uri $heartbeat -Headers $oldKey -SkipHttpErrorCheck).StatusCode
(Invoke-WebRequest -Method Post -Uri $heartbeat -Headers $deviceKey -SkipHttpErrorCheck).StatusCode
```

Urutannya **401 → 204 → 401 → 204**. `Invoke-WebRequest` memungkinkan kamu melihat status HTTP. `-SkipHttpErrorCheck` membuat PowerShell tetap menampilkan respons `401` yang sengaja kita cari. `204` berarti heartbeat berhasil dan server mengirim respons tanpa body. Simpan `$deviceKey` untuk uji berikutnya. Tampilkan statusnya saja saat berbagi layar.

Backend membuat key acak dan menyimpan hash SHA-256. Saat request masuk, backend menghitung hash dari header dan mencocokkannya dengan key aktif untuk perangkat yang aktif. Rotasi membuat hash baru dan mencabut key lama dalam satu transaksi.

**File dan fungsi.** [internal/handler/http.go](../internal/handler/http.go) `DeviceAuth` membaca header, sedangkan `Admin` memeriksa Bearer token. [internal/catalog/catalog.go](../internal/catalog/catalog.go) `newKey`, `hashKey`, `Authenticate`, dan `RotateKey` mengelola secret. [internal/catalog/postgres.go](../internal/catalog/postgres.go) `Authenticate` mencari hash dan `RotateKey` mencatat pencabutan.

## 3. Validasi input dan pencegahan duplikat

### A. Bukti replay dari dashboard dan PostgreSQL

1. Setelah kiriman pertama menampilkan tiga reading di histori, salin **Event ID lengkap**. Jalankan blok di bawah sebagai pengukuran pertama. Isi prompt dengan UUID event itu, misalnya `e6c8429c-f8f2-461a-9326-fab4d262d978`.
2. Klik **Ulangi event terakhir**. Tunjukkan `202` dan Event ID yang sama. Tahan klik pembacaan baru sampai pemeriksaan selesai.
3. Jalankan perintah cek Redis dan PostgreSQL di bagian bawah sekali lagi. Bila `XLEN` masih lebih dari nol, beri worker waktu lalu ulangi. Setelah nol, klik **Segarkan** dan pastikan SQL masih memberi **1 event, 3 readings, 3 sensor**.

```powershell
$eventId = [guid](Read-Host 'Tempel Event ID lengkap dari dashboard')
$sql = @"
SELECT COUNT(DISTINCT e.id) AS events,
       COUNT(r.id) AS readings,
       COUNT(DISTINCT r.sensor_id) AS sensors
FROM ingestion_events e
JOIN device_identifiers di ON di.device_id = e.device_id
LEFT JOIN sensor_readings r
    ON r.ingestion_event_id = e.id AND r.recorded_at = e.recorded_at
WHERE di.public_id = '$($demoDevice.id)'::uuid
  AND e.event_id = '$eventId'::uuid;
"@

docker compose exec -T redis redis-cli XLEN iot:readings
docker compose exec -T postgres psql -X -U iot_user -d iot_monitor -c $sql
```

`docker compose exec` menjalankan utilitas dalam container yang sedang aktif. `XLEN` menunjukkan panjang stream saat dicek. `psql` menjalankan SQL baca saja. Query mencari satu event milik perangkat UI lalu menghitung reading yang terkait. `[guid]` memastikan input Event ID berbentuk UUID. Kalau kamu ingin mengecek event baru, jalankan ulang bagian `Read-Host` dan pembuatan `$sql`. Query yang sudah dibuat menyimpan ID yang lama.

### B. Bukti validasi dengan dua request yang sengaja salah

Gunakan key CLI yang aktif dari requirement 2. Daftarkan sensor humidity untuk perangkat CLI. Kirim nilai `-1` dengan sensor miliknya, lalu kirim nilai CO₂ valid dengan ID sensor milik perangkat dashboard.

```powershell
$cliHumidity = Invoke-RestMethod -Method Post -Uri "$authUrl/sensors" -Headers $admin `
    -ContentType 'application/json' -Body '{"metric":"humidity"}'

function Send-DemoProbe([guid]$Sensor, [string]$Metric, [double]$Value) {
    $body = @{
        event_id = [guid]::NewGuid().ToString()
        recorded_at = [DateTime]::UtcNow.ToString('o')
        measurements = @(@{ sensor_id = $Sensor.ToString(); metric = $Metric; value = $Value })
    } | ConvertTo-Json -Depth 5 -Compress

    Invoke-WebRequest -Method Post -Uri "$base/readings" -Headers $deviceKey `
        -ContentType 'application/json' -Body $body -SkipHttpErrorCheck |
        Select-Object StatusCode,Content
}

Send-DemoProbe $cliHumidity.id 'humidity' -1
Send-DemoProbe $demoCo2.id 'co2' 1200
```

Fungsi lokal `Send-DemoProbe` hanya membantu membentuk payload dengan Event ID baru dan waktu UTC. Hasilnya **400** karena humidity wajib 0–100, lalu **403** karena sensor CO₂ itu milik perangkat lain.

**Penjelasan** Validasi terjadi sebelum enqueue. Deduplikasi terjadi saat worker memproses antrean. Worker membandingkan hash untuk pasangan `(device_id, event_id)`. Retry dengan payload identik bisa sama-sama menerima `202` dan tetap menghasilkan satu event tersimpan. Payload yang berbeda dengan ID yang sama dicatat sebagai penolakan permanen.

**File dan fungsi.** [internal/ingest/ingest.go](../internal/ingest/ingest.go) `Submit`, `ValidValue`, dan `payloadHash` memvalidasi lalu membuat identitas payload. [internal/ingest/postgres.go](../internal/ingest/postgres.go) `OwnedSensors` memeriksa pemilik sensor dan `PersistBatch` menyimpan event secara atomik. [internal/stream/worker.go](../internal/stream/worker.go) `handle` mengendalikan retry dan ack. Unique constraint berasal dari [migrasi awal](../migrations/000001_init.up.sql) dan [migrasi hypertable](../migrations/000006_readings_hypertable.up.sql).

## 4. Histori dan agregasi per menit serta per jam

Di UI, pilih **Per menit** di **Histori & agregasi**, lalu tunjukkan isi tabel **Mulai bucket / Rata-rata / Min / Maks / Jumlah**. Ganti ke **Per jam**. Bucket menit mulai pada batas menit reading, sedangkan bucket jam mulai pada batas jam yang sama. Setelah satu event dan satu replay identik, hitungan masih satu per sensor. Kalau datanya baru satu sampel, wajar rata-rata, min, dan maks sama.

### Bukti angka dengan tiga reading terkontrol

Ini bisa kamu tunjukkan bila  ingin melihat lebih dari dropdown. Pakai perangkat khusus agregasi agar angka tidak bercampur dengan data UI. Tiga reading CO₂ dibuat pada **jam UTC sebelumnya**. Nilai **600** dan **1.000** berada di menit 10. Nilai **1.100** berada di menit 11. Semua berada dalam satu jam.

```powershell
$aggDeviceBody = @{ name = "$demoName Agregasi"; location = 'Lab agregasi' } |
    ConvertTo-Json -Compress
$aggDevice = Invoke-RestMethod -Method Post -Uri "$base/devices" -Headers $admin `
    -ContentType 'application/json' -Body $aggDeviceBody
$aggUrl = "$base/devices/$($aggDevice.id)"
$aggSensor = Invoke-RestMethod -Method Post -Uri "$aggUrl/sensors" -Headers $admin `
    -ContentType 'application/json' -Body '{"metric":"co2"}'
$aggKey = @{ 'X-API-Key' = $aggDevice.api_key }

$now = [DateTime]::UtcNow
$hourStart = $now.Date.AddHours($now.Hour - 1)
$samples = @(
    @{ second = 605; value = 600 }
    @{ second = 635; value = 1000 }
    @{ second = 670; value = 1100 }
)
foreach ($sample in $samples) {
    $body = @{
        event_id = [guid]::NewGuid().ToString()
        recorded_at = $hourStart.AddSeconds($sample.second).ToString('o')
        measurements = @(@{ sensor_id = $aggSensor.id; metric = 'co2'; value = $sample.value })
    } | ConvertTo-Json -Depth 5 -Compress
    (Invoke-WebRequest -Method Post -Uri "$base/readings" -Headers $aggKey `
        -ContentType 'application/json' -Body $body).StatusCode
}
```

Bagian pertama membuat perangkat dan sensor. Perulangan memberi setiap reading Event ID sendiri. Tiga angka `202` berarti ketiga event berhasil antre. Blok berikut menunggu ketiga reading tersedia lalu meminta dua jenis agregasi dari **endpoint yang sama**. Query string `sensor_id` memilih satu sensor, `from` dan `to` membatasi satu jam pengujian, dan `bucket` menentukan lebar pengelompokan.

```powershell
$from = [Uri]::EscapeDataString($hourStart.ToString('o'))
$to = [Uri]::EscapeDataString($hourStart.AddHours(1).ToString('o'))
$filter = "sensor_id=$($aggSensor.id)&from=$from&to=$to"

$deadline = (Get-Date).AddSeconds(15)
do {
    $rows = Invoke-RestMethod "$aggUrl/readings?$filter" -Headers $admin
    if ($rows.Count -eq 3) { break }
    Start-Sleep -Milliseconds 500
} while ((Get-Date) -lt $deadline)
if ($rows.Count -ne 3) { throw 'Tiga reading belum tersimpan. Periksa worker.' }

foreach ($bucket in @('minute','hour')) {
    Write-Host "Bucket: $bucket (UTC)"
    Invoke-RestMethod "$aggUrl/aggregations?$filter&bucket=$bucket" -Headers $admin |
        Format-Table bucket_start,avg,min,max,count
}
```

| Bucket | Rata-rata | Min | Maks | Jumlah |
| --- | ---: | ---: | ---: | ---: |
| Menit 10 | 800 | 600 | 1.000 | 2 |
| Menit 11 | 1.100 | 1.100 | 1.100 | 1 |
| Satu jam | 900 | 600 | 1.100 | 3 |

API mengurutkan bucket terbaru terlebih dulu. PowerShell dapat menampilkan waktu dengan format lokal, dan dashboard menampilkan waktu lokal browser. Batas bucket di SQL dihitung dalam UTC. `COUNT` menghitung reading, dan rata-rata jam dihitung dari tiga nilai mentah.

**File dan fungsi.** [demo-ui/app.js](../demo-ui/app.js) `refresh()` meminta data, `renderHistory()` menampilkan tabel. [internal/handler/history.go](../internal/handler/history.go) `RegisterHistory` dan `historyFilter` membaca parameter. [internal/history/history.go](../internal/history/history.go) `Service.Aggregations` memilih interval. [internal/history/postgres.go](../internal/history/postgres.go) `Postgres.Aggregations` memakai `date_bin` dan SQL `AVG`, `MIN`, `MAX`, `COUNT` di atas tabel reading.

## 5. Alert berdasarkan threshold tiga sensor

Tunjukkan tiga alert **ACTIVE** dari pembacaan tinggi di requirement 1. Arahkan kursor ke badge alert jika ingin melihat Alert ID. Klik **Kirim pembacaan pemulihan**, tunggu worker selesai, lalu tunjukkan bahwa ketiga alert berstatus **RESOLVED**. UI akan mencoba memperbarui hasil selama beberapa detik. Klik **Segarkan** bila perlu.

| Sensor | Aturan pemicu `gt` | Nilai tinggi | Batas pulih `<=` | Nilai pemulihan |
| --- | ---: | ---: | ---: | ---: |
| CO₂ | > 1.000 ppm | 1.200 ppm | 900 ppm | 850 ppm |
| Suhu | > 26 °C | 31,5 °C | 25 °C | 25 °C |
| Kelembapan | > 60% RH | 74% RH | 55% RH | 55% RH |

**Penjelasan** Threshold dapat diatur per sensor dari API. Nilai di tabel berasal dari preset dashboard. Untuk CO₂, setelah alert aktif pada 1.200 ppm, nilai 950 ppm belum menutup alert. Alert baru pulih pada 900 ppm atau lebih rendah. Jarak antara ambang pemicu dan pemulihan disebut *hysteresis*. Database membatasi satu alert aktif per aturan, dan worker mengabaikan reading yang lebih lama untuk perubahan status alert.

**File dan fungsi.** [demo-ui/app.js](../demo-ui/app.js) `metrics` mendefinisikan preset dan `setup()` membuat tiga rule. [internal/handler/alert.go](../internal/handler/alert.go) `RegisterAlerts` memasang rute rule dan alert. [internal/alert/alert.go](../internal/alert/alert.go) `Service.CreateRule` memvalidasi aturan, `Policy.Triggers` dan `Policy.Recovers` membandingkan reading. [internal/alert/postgres.go](../internal/alert/postgres.go) `EvaluateBatch` dan `applyReadings` mengubah status dalam transaksi ingestion.

## 6. Monitoring online dan offline

1. Saat setup perangkat, requirement 1 sudah menunjukkan **NEVER_SEEN**. Pada tahap ini reading sebelumnya dapat membuat perangkat **ONLINE**, karena ingestion juga mencatat aktivitas.
2. Klik **Kirim heartbeat**. Tunjukkan **ONLINE** dan waktu **Terakhir terlihat** yang berubah.
3. Tunggu lebih dari 30 detik jika memakai konfigurasi demo. Selama menunggu, hindari pembacaan dan heartbeat baru. Klik **Segarkan** dan tunjukkan **OFFLINE**.
4. Klik **Kirim heartbeat** lagi dan tunjukkan **ONLINE**. Jika memakai pengaturan bawaan, waktu tunggu mengikuti `OFFLINE_TIMEOUT`, biasanya 5 menit.

```powershell
Invoke-RestMethod "$demoUrl/status" -Headers $admin |
    Format-List id,is_active,connectivity,last_seen_at
```

`GET /status` hanya membaca status. Perhatikan `is_active` tetap `True` ketika `connectivity` adalah `OFFLINE`. Yang pertama mengatur apakah perangkat diizinkan beroperasi, sedangkan yang kedua menyatakan seberapa baru perangkat beraktivitas.

**Penjelasan** Perangkat yang memanggil `POST /heartbeat` dan backend mencatat jam server. Worker monitoring memeriksa timestamp di database. Endpoint status juga menghitung status dari timestamp dan timeout sehingga UI bisa memperlihatkan hasil ketika disegarkan.

**File dan fungsi.** [internal/handler/monitor.go](../internal/handler/monitor.go) `RegisterHeartbeat` dan `RegisterDeviceStatus` memasang rute. [internal/monitor/postgres.go](../internal/monitor/postgres.go) `Heartbeat`, `ObserveBatch`, `Status`, dan `MarkOffline` menangani pembaruan serta pembacaan. [internal/monitor/monitor.go](../internal/monitor/monitor.go) `Service.Run` menjalankan pemeriksaan berkala.

## 7. Concurrent requests dan bukti beban

Tunjukkan **dua jenis bukti**. Integration test memeriksa bahwa request bersamaan tidak membuat data ganda. k6 mengukur throughput, latensi, dan error saat banyak perangkat mengirim data sekaligus. Dashboard satu perangkat menunjukkan alur fitur. Gunakan k6 untuk bukti kapasitas beban yang terukur.

### Cara presentasikan angka yang sudah ada

Benchmark fleet setelah migrasi TimescaleDB memakai **1.000 perangkat selama 60 detik**, masing-masing dengan tiga sensor. Hasil di host lokal tersebut adalah **66.458 request**, sekitar **1.086 request per detik**, **p95 3,05 ms**, **p99 8,34 ms**, dan **0% error**. Setelah drain, **60.783 event unik** dan **182.349 reading** tersimpan, termasuk verifikasi **5.675 replay**, dengan nol duplikat. Backlog sampel tertinggi 254 dan backlog akhir nol. Alert akhir cocok dengan profil tes, yakni 600 aktif dan 600 selesai tanpa duplikat.

```powershell
$proofPath = '.\artifacts\fleet-load\1000-09cb09d4-66f8-42e8-bdc8-b5a2e37c834f\result.json'
$proof = Get-Content -LiteralPath $proofPath -Raw | ConvertFrom-Json
$proof | Select-Object devices,nominal_seconds,requests,requests_per_second,p95_ms,p99_ms,`
    error_rate,peak_sampled_backlog,final_backlog,final_pending,confirmed_replays
$proof.reconciliation | Select-Object accepted_ledger,persisted_events,persisted_readings,`
    missing_events,duplicate_readings,active_alerts,resolved_alerts,duplicate_alerts
```

`ConvertFrom-Json` membaca file hasil mentah. `accepted_ledger` mencatat event unik yang diterima, dan `persisted_events` adalah jumlah yang ditemukan PostgreSQL. `p95 3,05 ms` berarti sekitar 95% sampel durasi HTTP selesai sampai nilai tersebut. Latensi ini mengukur respons API, sedangkan rekonsiliasi setelah drain membuktikan penyimpanan. Folder `artifacts/` bersifat lokal dan diabaikan Git. Jika memakai clone baru, sertakan file hasil atau jalankan skenario sendiri.

**Jika ditanya hasil awal.** Pada uji burst **satu perangkat, 1.000 VU**, uji pertama mendapat **66,90% error**. Dalam pengujian ulang berpasangan dengan konfigurasi sama, error turun **67,36% → 0%** setelah batching. Request yang mendapat `202` naik dari sekitar **1.263 → 3.835 per detik**, dan puncak antrean turun dari **10.000 → 307**. Pada awalnya worker membuka satu transaksi per event. Batching menyatukan hingga 32 pesan dalam satu transaksi, jadi lebih sedikit transaksi dan pembaruan aktivitas yang berulang. Uji burst satu perangkat dan uji fleet adalah dua skenario berbeda. Bukti uji berpasangan ada di `artifacts/load/1000-3b68d420-cb6b-420b-ab4c-045994e2bb7e/result.json` dan `artifacts/load/1000-2bb7cba8-5e5f-46e8-9982-4a810597026d/result.json`.

### Terminal C · Jalankan benchmark di stack terpisah

Buka terminal C dari root repo. Tiga port ini diakses dari host dan dipilih agar stack pengujian dapat berjalan bersama dashboard. Gunakan port lain bila sudah dipakai. Nama proyek Compose memisahkan network, container, dan volume pengujian.

```powershell
$env:API_PORT = '18082'
$env:POSTGRES_PORT = '15433'
$env:REDIS_PORT = '16379'
$testProject = 'iot-demo-tests'

docker compose -p $testProject up --build --wait
if ($LASTEXITCODE -ne 0) { throw 'Stack pengujian belum siap.' }

.\scripts\fleet-load-test.ps1 -Project $testProject -Devices 100 -Seconds 60
```

Runner membuat 100 perangkat beserta sensor dan rule, menjalankan k6 di Docker, menunggu antrean habis, lalu memeriksa hasil dengan SQL. Durasi kirim 60 detik dan keseluruhan run bisa lebih lama karena persiapan dan drain. k6 dijalankan di container, sehingga instalasi k6 di Windows tidak diperlukan. Setelah keluar pesan `Results saved to ...`, salin path folder tersebut dan pakai sebagai sumber `result.json`. Untuk tiga ukuran sekaligus, gunakan `-Devices 100,500,1000` dan sediakan waktu lebih panjang.

Contoh hasil latihan 100 perangkat dari perintah di atas. k6 mencatat **6.678 request, 0% error, 6.107 event unik, dan 18.321 reading tersimpan**. Rekonsiliasi menemukan nol event hilang, reading duplikat, dan alert duplikat. Angkanya bisa dilihat di [hasil mentah latihan](../artifacts/fleet-load/100-f6f3a449-1cfa-4b10-a476-c3ea1875cefd/result.json).

```powershell
.\scripts\fleet-worker-recovery.ps1 -Project $testProject -BaseUrl 'http://127.0.0.1:18082'
.\scripts\dependency-recovery-test.ps1 -Project $testProject -BaseUrl 'http://127.0.0.1:18082'
```

Skrip pertama menghentikan worker secara paksa saat ada pesan pending dan memeriksa bahwa consumer baru mengambil pesan itu kembali. Skrip kedua menghentikan PostgreSQL dan melakukan restart terkontrol Redis secara bergantian. Ia memeriksa `503` dan `Retry-After` saat dependency sedang gagal serta pemulihan setelah layanan hidup. Hasilnya ditulis ke `artifacts/fleet-recovery/` dan `artifacts/dependency-recovery/`.

**File dan fungsi.** [internal/stream/worker.go](../internal/stream/worker.go) `Worker.Run` memulai consumer paralel dan `handle` memproses batch. [internal/stream/stream.go](../internal/stream/stream.go) `Queue.Enqueue` menerapkan batas antrean, dan `Read`, `Claim`, `Ack` mengelola pengiriman ulang serta konfirmasi. [internal/ingest/postgres.go](../internal/ingest/postgres.go) `PersistBatch` menjaga konsistensi transaksi. [scripts/fleet-load-test.ps1](../scripts/fleet-load-test.ps1) `Invoke-FleetScenario` mengorkestrasi tes, [scripts/fleet-provision.js](../scripts/fleet-provision.js) `setup()` mendaftarkan perangkat, [scripts/fleet-ingest.js](../scripts/fleet-ingest.js) `generateReadings()` membuat nilai dan `send()` mengirimnya, lalu [scripts/verify-fleet.sql](../scripts/verify-fleet.sql) memeriksa penyimpanan serta alert.

## 8. Unit test dan integration test

### A. Unit test saja

```powershell
go test -count=1 -v ./internal/ingest ./internal/alert ./internal/handler
```

`go test` menemukan fungsi `Test...` di file `*_test.go`. `-count=1` memaksa eksekusi baru, dan `-v` menampilkan nama serta status tiap tes. Tiga path ini memilih package yang punya unit test. Docker dan dashboard bisa ditutup saat menjalankan langkah ini.

| File | Fungsi tes dan yang diperiksa |
| --- | --- |
| [internal/ingest/ingest_test.go](../internal/ingest/ingest_test.go) | `TestValidValue` memeriksa batas nilai sensor. `TestCanonicalPayload` memastikan payload ekuivalen menghasilkan hash yang sama. |
| [internal/alert/alert_test.go](../internal/alert/alert_test.go) | `TestPolicy` memeriksa aturan pemicu dan pemulihan alert. |
| [internal/handler/http_test.go](../internal/handler/http_test.go) | `TestWriteErrorDependencyStatus` memeriksa status error HTTP, termasuk `503` dan header `Retry-After`. |

Cari **empat hasil `--- PASS: Test...`**. Beberapa fungsi memiliki subtes. `ok` di akhir baris package berarti package tersebut lulus.

### B. Integration test dengan PostgreSQL dan Redis nyata

Gunakan **terminal C** dan stack pengujian yang sudah dibuat di requirement 7. Kalau melewati benchmark, jalankan bagian `docker compose -p $testProject up --build --wait` beserta penyiapan ketiga portnya saja. Tes Go berbicara langsung ke PostgreSQL dan Redis melalui port host. `httptest` memasukkan request ke router Gin dari dalam proses tes sehingga dashboard dan koneksi HTTP ke port API tidak diperlukan.

```powershell
$pgPassword = $env:POSTGRES_PASSWORD
if (-not $pgPassword) {
    $pgLine = Get-Content -LiteralPath '.env' |
        Where-Object { $_ -like 'POSTGRES_PASSWORD=*' } | Select-Object -First 1
    $pgPassword = $pgLine -replace '^POSTGRES_PASSWORD=', ''
}
if (-not $pgPassword) { throw 'POSTGRES_PASSWORD belum diisi.' }

$pgUrlPassword = [Uri]::EscapeDataString($pgPassword)
$env:TEST_DATABASE_URL = "postgres://iot_user:$pgUrlPassword@127.0.0.1:$($env:POSTGRES_PORT)/iot_monitor?sslmode=disable"
$env:TEST_REDIS_URL = "redis://127.0.0.1:$($env:REDIS_PORT)/0"
$env:GIN_MODE = 'release'

go test -count=1 -v ./integration
```

`$env:...` menetapkan variabel yang dibaca proses Go. `EscapeDataString` mengubah karakter khusus password ke bentuk yang aman di URL. Contoh menggunakan akun `iot_user` dan database `iot_monitor`. Jika kedua URL tes kosong, suite tampil sebagai **SKIP**. Jika hanya satu URL ada, tes gagal karena konfigurasi belum lengkap. Jalankan `go test -count=1 -v ./...` setelah dua URL terisi untuk menjalankan unit dan integrasi bersama.

**Cara baca satu tes saat demo.** Buka [integration/backend_test.go](../integration/backend_test.go) dan cari `TestRollbackAndConcurrentDuplicateInsert`. Setup membuat schema PostgreSQL dan stream Redis khusus untuk tiap tes. Tes menjalankan operasi konkuren dan memeriksa isi database. Fungsi `setupWithLegacyData` menyiapkan service nyata dan memakai migrasi, sedangkan `fixture.request` melewatkan request uji ke router dengan `httptest`. Cleanup menghapus data uji yang terisolasi.

Untuk menjalankan fungsi itu saja setelah dua URL tes terisi, pakai `go test -count=1 -v ./integration -run '^TestRollbackAndConcurrentDuplicateInsert$'`. Ini cara singkat memperlihatkan pemeriksaan konkuren di terminal.

| Fungsi di `integration/backend_test.go` | Bukti yang diperiksa |
| --- | --- |
| `TestCatalogAndAuthentication` | Registrasi, kredensial, dan rotasi ID/key. |
| `TestQueueValidationDedupAndDeadLetters` | Validasi, kepemilikan sensor, replay, konflik dan penolakan. |
| `TestRedisFailureReturns503` | Gangguan Redis pada jalur API menghasilkan `503`. |
| `TestAuthenticationDatabaseSchemaErrorReturns500` | Kesalahan skema yang bersifat internal tetap `500`. |
| `TestRollbackAndConcurrentDuplicateInsert` | Rollback dan deduplikasi saat insert bersamaan. |
| `TestBatchConflictIsolatesNeighbors` | Event konflik di batch tidak menghalangi penyimpanan event valid. |
| `TestBatchAlertTransitionsIgnoreOlderReadings` | Reading lama tidak mengubah status alert yang lebih baru. |
| `TestWorkerLeavesFailurePendingAndReclaims` | Kegagalan penyimpanan meninggalkan pesan pending dan pesan bisa diambil ulang. |
| `TestAlertOrderingAndUTCQuery` | Urutan alert, histori, dan agregasi UTC menit/jam. |
| `TestConnectivityAndConcurrentSweeps` | Heartbeat, offline, dan sweep konkuren. |
| `TestVersionOneUpgradePreservesRecords` | Migrasi dari skema lama menjaga data yang sudah ada. |

Hasil terakhir yang disimpan ada di [`go-test-final.log`](../artifacts/timescale-upgrade/20261120/go-test-final.log). Race detector juga lulus lewat image Docker target `test`.

```powershell
docker build --target test -t iot-demo-race:local .
if ($LASTEXITCODE -ne 0) { throw 'Build image race test gagal.' }

$hostTestDatabase = $env:TEST_DATABASE_URL
$hostTestRedis = $env:TEST_REDIS_URL
try {
    $env:TEST_DATABASE_URL = "postgres://iot_user:$pgUrlPassword@postgres:5432/iot_monitor?sslmode=disable"
    $env:TEST_REDIS_URL = 'redis://redis:6379/0'
    docker run --rm --network "$($testProject)_default" -e TEST_DATABASE_URL -e TEST_REDIS_URL `
        -e GIN_MODE=release iot-demo-race:local
    if ($LASTEXITCODE -ne 0) { throw 'Race test gagal.' }
} finally {
    $env:TEST_DATABASE_URL = $hostTestDatabase
    $env:TEST_REDIS_URL = $hostTestRedis
}
```

Container memakai nama service `postgres` dan `redis` karena berada di network Compose. Target `test` memasang compiler C yang diperlukan oleh `go test -race`. Race detector mencari akses ke memori Go yang saling bertabrakan. k6 mengukur beban HTTP dan kebenaran data setelah drain.

## 9. Docker untuk deployment

Tunjukkan ini di **terminal B** agar yang diperiksa adalah stack utama. `docker compose ps -a` menunjukkan apakah komponen berjalan. Endpoint readiness memeriksa akses API ke dependency. Perintah berikutnya menanyakan versi migrasi dan daftar hypertable langsung ke PostgreSQL.

```powershell
docker compose ps -a
Invoke-RestMethod 'http://127.0.0.1:8080/health/ready'

docker compose exec -T postgres psql -X -U iot_user -d iot_monitor `
    -c 'SELECT version, dirty FROM schema_migrations;' `
    -c "SELECT extversion FROM pg_extension WHERE extname = 'timescaledb';" `
    -c "SELECT hypertable_name, num_chunks FROM timescaledb_information.hypertables WHERE hypertable_schema = 'public' AND hypertable_name = 'sensor_readings';"
```

| Yang ditunjukkan | Maknanya |
| --- | --- |
| `postgres` healthy | Container PostgreSQL 17 menjalankan TimescaleDB. Volume `timescale-data` menyimpan data di luar umur container. |
| `redis` healthy | Redis menyimpan antrean dan consumer group. AOF ditulis ke volume `redis-data`, dengan sinkronisasi berkala. |
| `migrate` Exited (0) | Container sekali jalan sudah menyelesaikan migrasi SQL. |
| `api` healthy | Binary `/app/api` melayani Gin dan health endpoint. |
| `worker` Up | Binary `/app/worker` menjalankan consumer dan monitoring. Reading yang berubah dari `queued` menjadi tersimpan adalah bukti consumer bekerja. |
| `version = 6`, `dirty = f`, `extversion = 2.32.1`, `sensor_readings` tercantum | Migrasi bersih, ekstensi aktif, dan tabel reading menjadi hypertable. Jumlah chunk bergantung pada tanggal data yang tersimpan. |

**Penjelasan** `Dockerfile` membangun dua binary dari source Go dan menyalinnya ke image runtime yang sama. `compose.yaml` menjalankan image tersebut sebagai API dan worker dengan perintah berbeda, menyiapkan database dan Redis, serta menunggu health check dan hasil migrasi sebelum menyalakan backend. Named volume mempertahankan data ketika container dibuat ulang. Dashboard demo berjalan lokal lewat `go run`.

**File dan fungsi.** [Dockerfile](../Dockerfile) berisi tahap build, test, dan runtime. [compose.yaml](../compose.yaml) mendefinisikan lima service, volume, dan urutan startup. [cmd/api/main.go](../cmd/api/main.go) `run` menyiapkan server, dan [cmd/worker/main.go](../cmd/worker/main.go) `run` menyiapkan consumer dan monitor. [internal/handler/health.go](../internal/handler/health.go) `HealthReady` memeriksa dependency. [migrations/000006_readings_hypertable.up.sql](../migrations/000006_readings_hypertable.up.sql) mengaktifkan TimescaleDB dan mengonversi tabel reading.

## 10. Dokumentasi API dan arsitektur

Buka [docs/openapi.json](openapi.json) di editor atau impor ke Swagger Editor. Tunjukkan tiga operasi untuk memperlihatkan perbedaan akses.

- `POST /api/v1/readings` memakai `X-API-Key`. Tunjukkan body, contoh reading, serta `202`, `400`, `401`, `403`, dan `503`.
- `GET /api/v1/devices/{id}/aggregations` memakai Bearer token dengan parameter `bucket=minute` atau `bucket=hour`.
- `GET /health/ready` digunakan Docker untuk mengecek kesiapan API.

Kontrak saat ini mendefinisikan **23 operasi HTTP dalam 18 path**. Metode berbeda di path sama dihitung sebagai operasi terpisah. Jika ingin memastikan hitungannya, blok ini membaca JSON dan menghitung jumlah metode.

```powershell
$spec = Get-Content -LiteralPath 'docs/openapi.json' -Raw | ConvertFrom-Json -AsHashtable
$httpMethods = @('get','post','patch','put','delete','head','options','trace')
$ops = @(foreach ($path in $spec.paths.Values) {
    $path.Keys | Where-Object { $_ -in $httpMethods }
})
[pscustomobject]@{ paths = $spec.paths.Count; operations = $ops.Count }
```

**File dan fungsi.** [docs/openapi.json](openapi.json) adalah kontrak. [internal/router/router.go](../internal/router/router.go) `NewWithOptions` menghubungkan middleware, handler, dan service. [README.md](../README.md) menjelaskan cara menjalankan stack. Untuk diagram arsitektur, gunakan diagram pipeline dan ERD yang kamu siapkan dan tunjukkan jalur API → Redis → worker → PostgreSQL.

## Menutup demo

Di terminal C, hentikan **proyek uji** setelah selesai. Volume tetap tersedia untuk pengujian berikutnya.

```powershell
docker compose -p $testProject stop
```

Di terminal A, tekan **Ctrl+C** untuk menghentikan dashboard. Jika kamu ingin mengembalikan timeout demo ke konfigurasi `.env`, hapus override di terminal A lalu jalankan ulang service API dan worker.

```powershell
Remove-Item Env:OFFLINE_TIMEOUT,Env:MONITOR_INTERVAL -ErrorAction SilentlyContinue
docker compose up --wait api worker
```

