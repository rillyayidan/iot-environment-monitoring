# Laporan pengujian

Pengujian dilakukan pada 19 November 2026 dengan Windows PowerShell, Docker Desktop, Go 1.27.4, PostgreSQL 17, Redis 7.4, dan k6 1.4.2. Docker menyediakan 12 CPU logis dan sekitar 7,43 GiB RAM. Stack Compose menjalankan 4 consumer Go dan batas 10.000 pesan di Redis.

## Pemeriksaan fungsi

| Pemeriksaan | Hasil |
| --- | --- |
| `go test -count=1 ./...` dan pengujian ulang integration suite | Lulus. Ketiga unit test dan delapan integration test lulus. |
| `go test -race -count=1 -v ./...` | Lulus di Linux amd64 melalui Docker dengan PostgreSQL dan Redis nyata. |
| `go build ./...`, `go vet ./...`, `go mod verify` | Lulus. |
| `docker compose config --quiet` dan `docker compose up --build --wait` | Lulus. PostgreSQL, Redis, dan readiness API merespons. `schema_migrations` melaporkan versi 5 tanpa dirty flag. |
| Validator OpenAPI | `docs/openapi.json` lulus validasi OpenAPI 3.1. |
| Uji Windows PowerShell | Contoh di `docs/api.md` berjalan dengan API Compose, termasuk heartbeat, ingestion, histori, alert, rotasi, dan simulator. |
| Batas enqueue dan commit pada Compose | Worker dihentikan. API mengembalikan `202` dan histori berisi nol reading untuk event uji. Setelah worker dinyalakan, satu reading tersimpan. |

Cakupan meliputi registrasi dan rotasi perangkat/sensor, autentikasi dan pencabutan key, pemilikan sensor, batas nilai, Redis gagal, queue penuh, replay dan konflik event, deduplikasi konkuren, rollback, pending reclaim dan acknowledgment, hysteresis dan out-of-order readings, agregasi menit/jam, konektivitas konkuren, serta pemeliharaan data lama saat migrasi.

## Hasil k6

Setiap skenario memakai durasi 10 detik, 250 ms think time, satu device dan satu sensor, serta replay identik setiap sepuluh iterasi. Satu device sengaja menjadi titik beban untuk jalur ingestion dan pembaruan `last_seen_at`. RPS dan latensi menghitung semua request. Reading disensus sesudah drain. Backlog puncak dihitung dari sampel `XLEN`.

| VU | Request | Request/detik | p95 (ms) | p99 (ms) | Error | Event unik mendapat `202` | Reading tersimpan | Puncak antrean teramati |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 100 | 4.301 | 419,24 | 6,14 | 140,31 | 0,00% | 3.910 | 3.910 | 73 |
| 500 | 21.467 | 2.098,60 | 9,09 | 79,49 | 34,95% | 12.800 | 12.800 | 10.000 |
| 1.000 | 38.175 | 3.717,17 | 113,91 | 142,79 | 66,90% | 11.646 | 11.646 | 10.000 |
| 2.000 | 42.453 | 4.053,90 | 422,40 | 495,31 | 71,95% | 11.013 | 11.013 | 10.000 |

Skenario 100, 500, 1.000, dan 2.000 VU masing-masing mempunyai 391, 1.952, 3.471, dan 3.860 percobaan replay. Semua skenario berakhir dengan antrean kosong dan nol grup reading duplikat. Jumlah event dan reading yang tersimpan cocok dengan jumlah event unik yang menerima `202` pada tiap skenario. Antrean mencapai batas 10.000 pada 500 VU ke atas. Semakin banyak request gagal pada skenario tersebut, jadi RPS total mencakup beban yang ditolak dan tidak bisa dibaca sebagai throughput persistensi.

## Puncak sampel CPU dan memori Docker

Angka berikut berasal dari `docker stats --no-stream` selama pengiriman dan drain. CPU dapat melebihi 100% jika container memakai lebih dari satu core. Puncak CPU dan RAM untuk satu service bisa berasal dari sampel yang berbeda.

| VU | API CPU / MiB | Worker CPU / MiB | PostgreSQL CPU / MiB | Redis CPU / MiB | Sampel |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 100 | 34,46% / 23,42 | 62,52% / 8,12 | 65,00% / 83,01 | 23,74% / 6,88 | 6 |
| 500 | 148,50% / 38,20 | 68,10% / 8,33 | 145,23% / 92,38 | 36,52% / 13,31 | 14 |
| 1.000 | 296,90% / 62,71 | 68,42% / 8,32 | 224,72% / 103,00 | 52,91% / 11,75 | 13 |
| 2.000 | 330,13% / 112,80 | 82,68% / 8,70 | 232,70% / 107,00 | 57,23% / 11,91 | 13 |

Data mentah tersedia pada direktori lokal `artifacts/load`. Gunakan [panduan pengujian](testing.md) untuk mengulang skenario dengan durasi atau konfigurasi berbeda.

## Pengujian ulang setelah batching worker

Baseline dijalankan ulang dari image implementasi awal. Kedua build memakai host yang sama, empat consumer, kapasitas 10.000 pesan, dan AOF Redis `everysec`. Skenario 10 detik mempertahankan satu perangkat, satu sensor, replay setiap sepuluh iterasi, dan jeda 250 ms. Baseline mengambil satu pesan dan membuka satu transaksi per giliran. Build baru mengambil hingga 32 pesan dan mengelompokkan transaksi serta acknowledgment.

| VU | Build | HTTP/detik | HTTP 202/detik | HTTP 503 | Error | p95 / p99 (ms) | Reading tersimpan | Puncak antrean teramati |
| ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1.000 | Baseline | 3.868,70 | 1.262,87 | 26.715 | 67,36% | 79,30 / 254,65 | 11.958 | 10.000 |
| 1.000 | Batch 32 | 3.835,27 | 3.835,27 | 0 | 0,00% | 90,63 / 239,10 | 35.746 | 307 |
| 2.000 | Baseline | 4.689,98 | 1.222,02 | 35.939 | 73,94% | 288,84 / 341,26 | 11.769 | 10.000 |
| 2.000 | Batch 32 | 5.274,35 | 5.274,35 | 0 | 0,00% | 224,36 / 250,22 | 49.869 | 339 |

Throughput yang mendapat `202` meningkat sekitar 3,04× pada 1.000 VU dan 4,32× pada 2.000 VU. Antrean akhirnya kosong dan semua event unik yang menerima `202` cocok dengan jumlah reading tersimpan. Semua replay yang dikirim oleh build batch menerima `202` untuk dua request, masing-masing 3.575 pasangan pada 1.000 VU dan 4.987 pasangan pada 2.000 VU. SQL menemukan nol reading duplikat.

Latensi dihitung untuk semua respons, termasuk `503` dari baseline. Skenario VU tertutup dipengaruhi oleh waktu respons. Pengukuran laju kedatangan tetap di bawah menguji throughput dengan jadwal independen.

Empat hasil mentah perbandingan tersimpan di `artifacts/load` dengan ID berikut.

| Build | 1.000 VU | 2.000 VU |
| --- | --- | --- |
| Baseline | `1000-3b68d420-cb6b-420b-ab4c-045994e2bb7e` | `2000-d1786b01-186e-4db2-8b6c-1095e34bf04f` |
| Batch 32 | `1000-2bb7cba8-5e5f-46e8-9982-4a810597026d` | `2000-386df141-e36d-4310-8550-e881ebf6922d` |

### Laju tetap selama 60 detik

Pada jadwal 3.000 event baru/detik ditambah replay, k6 mengirim 198.003 request dan mengukur 3.299,71 HTTP/detik. Semua request mendapat `202`, seluruh jadwal k6 terlaksana, p95 11,56 ms, dan p99 62,15 ms. PostgreSQL menyimpan 180.002 event dan 180.002 reading. Seluruh 18.001 pasangan replay mendapatkan dua respons `202` dan SQL menemukan nol reading duplikat. Antrean mencapai 226 pesan pada sampel tertinggi dan kosong setelah drain. Hasil mentah ada di `artifacts/load/1000-a38f0f0c-dd64-49ba-aac4-444926250793`.

Puncak dari 18 sampel Docker pada pengujian 60 detik adalah API 250,59% CPU dan 105,00 MiB, worker 69,95% dan 8,72 MiB, PostgreSQL 248,79% dan 213,40 MiB, serta Redis 52,03% dan 11,76 MiB. PostgreSQL melaporkan nol deadlock pada database uji setelah benchmark.

Skenario ini menguji ingestion dan penulisan raw readings pada satu perangkat. Tes integrasi terpisah menguji alert aktif, rollback batch, isolasi konflik, batch yang saling tumpang tindih, dan pemulihan pesan pending. Kapasitas untuk campuran trafik histori, agregasi, dan alert aktif memerlukan pengukuran tersendiri.

Sepuluh tes integrasi PostgreSQL dan Redis lulus pada Windows. `go vet ./...`, `go build ./...`, dan `go test -race -count=1 ./...` juga lulus. Race detector berjalan melalui Docker Linux dengan PostgreSQL dan Redis nyata.

## Fleet multi-perangkat dengan alert aktif

Pengujian pada 19 November 2026 memakai Docker Desktop lokal dengan 12 CPU logis dan sekitar 7,43 GiB RAM, PostgreSQL 17.12, Redis 7.4.12, dan k6 1.4.2. Stack khusus pengujian memakai empat worker, batch maksimal 32, batas 10.000 pesan, AOF `everysec`, `CLAIM_IDLE=30s`, dan `PROCESS_TIMEOUT=10s`.

Setiap VU memakai perangkat, API key, serta satu sensor CO₂, satu sensor temperatur, dan satu sensor kelembapan yang unik. Provisioning lewat API berlangsung di proses k6 terpisah sebelum pengukuran. Ketiga skenario menjalankan `constant-vus` selama 60 detik. Jeda awal berbeda per perangkat, interval kirim berkisar 0,8–1,2 detik, dan setiap sepuluh event perangkat mengirim replay identik. Setiap event membawa tiga pengukuran.

Sebanyak 60% perangkat tetap normal, 20% melewati ambang lalu pulih, dan 20% mengalami kenaikan yang bertahan sampai akhir. Angka pengukuran bervariasi secara deterministik per perangkat dan waktu. Perubahan lingkungan dipercepat dalam jendela 60 detik. Ambang pemicu dan pemulihan masing-masing adalah 1.000/900 ppm, 30/28 °C, dan 70/65% RH. [Cara menjalankan ulang](testing.md#fleet-multi-perangkat-dan-pemulihan-worker)

| Perangkat | HTTP request | RPS k6 | p95 / p99 HTTP (ms) | Error | Puncak backlog / pending Redis | Backlog / pending akhir |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 100 | 6.678 | 109,17 | 2,08 / 2,44 | 0,00% | 3 / 3 | 0 / 0 |
| 500 | 33.251 | 543,41 | 2,05 / 3,47 | 0,00% | 15 / 13 | 0 / 0 |
| 1.000 | 66.451 | 1.086,28 | 2,70 / 5,75 | 0,00% | 94 / 83 | 0 / 0 |

RPS memakai durasi pengukuran k6 sekitar 61,17–61,19 detik, termasuk penyelesaian iterasi terakhir setelah jendela pengiriman 60 detik. HTTP latency mengukur respons enqueue. Redis disampel dengan satu operasi atomik per pengamatan. Backlog menghitung pesan yang masih berada di stream, termasuk pending. Puncak di tabel adalah puncak sampel.

| Perangkat | Replay dengan dua `202` | Event unik diterima dan tersimpan | Reading tersimpan | Alert aktif | Alert pulih |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 100 | 571 | 6.107 | 18.321 | 60 | 60 |
| 500 | 2.839 | 30.412 | 91.236 | 300 | 300 |
| 1.000 | 5.675 | 60.776 | 182.328 | 600 | 600 |

Seluruh UUID event unik yang menerima `202` ditemukan di PostgreSQL. Timestamp dan nilai ketiga pengukuran cocok dengan ledger. Tidak ada event tambahan, reading duplikat, alert duplikat, salah state, atau dead letter. Pengujian juga memverifikasi satu API key unik per perangkat, tiga metrik per perangkat, satu aturan aktif per sensor, dan partisipasi seluruh perangkat. Setiap alert diuji terhadap reading sumber yang memenuhi ambang trigger dan, untuk alert pulih, ambang pemulihan. Perangkat profil normal menghasilkan nol alert.

### Pemulihan worker

Database uji menahan lock sementara pada `ingestion_events`. API tetap menerima event asli dan replay dengan dua respons `202`. Dua transaksi worker terlihat menunggu lock. Saat worker dihentikan dengan `SIGKILL`, Docker mencatat exit code 137 dan kedua pesan tetap pending. Setelah worker dinyalakan kembali, ID pesan yang sama berpindah ke consumer baru dengan delivery count naik dari 1 menjadi 2 untuk keduanya. Setelah lock dilepas, PostgreSQL menyimpan satu event, tiga reading yang benar, dan tiga alert aktif pada tiga aturan berbeda. Backlog dan pending Redis berakhir di nol.

Pengamatan setelah kill hingga reclaim berjarak 29,956 detik. Pengamatan berikutnya menunjukkan antrean kosong 0,612 detik setelah reclaim. Suite Go lulus dengan PostgreSQL dan Redis nyata. PostgreSQL mencatat nol deadlock pada database pengujian.

| Skenario | Direktori hasil mentah |
| --- | --- |
| 100 perangkat | `artifacts/fleet-load/100-aeb4f078-694e-4dc7-b727-e574e5b99154` |
| 500 perangkat | `artifacts/fleet-load/500-825bb9af-4fd9-4f72-8679-44bf338d358b` |
| 1.000 perangkat | `artifacts/fleet-load/1000-df588393-7103-4372-b00d-92a39a05aa8d` |
| Pemulihan | `artifacts/fleet-recovery/309853b3-7938-45ec-bbd3-7c8e3910eaf5` |

Setiap direktori fleet berisi sampel k6 sebagai CSV gzip, ringkasan JSON, ledger event, manifest publik tanpa key, dan sampel Redis. Direktori recovery menyimpan hasil serta urutan perubahan pending list. Pengujian memakai data sintetis di satu host lokal dan cadence yang ditetapkan per perangkat. Uji crash membuktikan retry pada kegagalan worker saat PostgreSQL dan Redis tetap berjalan. Hasil mencakup jendela 60 detik per skenario dan belum mengukur batas maksimum, beban jangka panjang, atau kegagalan host.

Perubahan terbatas pada skrip provisioning dan ingestion k6, runner PowerShell, verifikasi SQL, serta dokumentasi. Empat worker dengan batch 32 mencukupi untuk cadence yang diuji.

## Kegagalan dependency

### Baseline sebelum perbaikan status HTTP

Dua skenario memakai stack Compose terisolasi dengan PostgreSQL 17.12, Redis 7.4.12, AOF `everysec`, dan `CLAIM_IDLE=30s`. Setiap skenario menahan commit dengan lock PostgreSQL sementara sampai event dan replay identiknya tampak sebagai dua pending message Redis. API telah mengembalikan `202` untuk keduanya. Kemudian satu dependency dihentikan, satu request baru dicoba, dan dependency tersebut dinyalakan kembali.

| Dependency | Liveness saat berhenti | Readiness saat berhenti → pulih | Ingestion baru saat berhenti → pulih | Stop hingga readiness pulih | Stop hingga backlog kosong |
| --- | ---: | ---: | ---: | ---: | ---: |
| PostgreSQL | 200 | 503 → 200 | 500 → 202 | 6,861 detik | 30,222 detik |
| Redis | 200 | 503 → 200 | 503 → 202 | 7,413 detik | 27,842 detik |

Waktu dihitung sejak konfirmasi stop container dan mencakup jeda outage yang disengaja. Status `500` pada outage PostgreSQL berasal dari request ingestion yang gagal saat akses database. Request tersebut memiliki nol event tersimpan. Saat Redis berhenti, ingestion mengembalikan `503` dan event yang ditolak juga memiliki nol event tersimpan.

Pada outage PostgreSQL, kedua ID pending tetap ada di Redis hingga database kembali. Pada restart Redis, server mencatat fsync saat shutdown dan memuat database dari AOF saat startup. Kedua ID pending kembali dengan delivery count 1. Worker mengambil ulang ID yang sama dan menaikkan kedua delivery count menjadi 2 sebelum lock PostgreSQL dilepas.

Pada setiap skenario, snapshot sebelum request baru pascapemulihan menunjukkan satu event, tiga reading, dan tiga alert aktif. Setelah satu event baru diterima, PostgreSQL berisi dua event, enam reading sesuai nilai kiriman, dan tepat tiga alert yang pulih sesuai aturan. Backlog, pending, dan dead letter berakhir di nol. Waktu mulai API dan worker tetap sama sepanjang pengujian, sehingga pemulihan terjadi melalui reconnect kedua proses. `go test -count=1 ./...` lulus setelah kedua simulasi dengan PostgreSQL dan Redis nyata.

| Skenario | Bukti yang tersimpan |
| --- | --- |
| PostgreSQL unavailable | `artifacts/dependency-recovery/postgres-a7b220d6-2fe7-4048-b417-338ab276cf96/result.json` |
| Redis restart | `artifacts/dependency-recovery/redis-69b0af3a-8989-410c-87a2-9156c291871c/result.json` dan `redis-server.log` |

Pengujian Redis memakai shutdown terkontrol dan volume yang sama. Risiko hilangnya tulis yang belum di-fsync saat host gagal tetap berlaku pada AOF `everysec`. [Cara mengulang](testing.md#postgresql-unavailable-dan-redis-restart) · [Redis Persistence](https://redis.io/docs/latest/operate/oss_and_stack/management/persistence/)

### Verifikasi setelah perbaikan status HTTP

Kedua skenario diulang pada image baru. Semua respons sementara dalam tabel menyertakan `Retry-After: 2`.

| Dependency yang dihentikan | Readiness | Ingestion | Heartbeat dengan API key | Pembacaan perangkat oleh admin |
| --- | ---: | ---: | ---: | ---: |
| PostgreSQL | 503 | 503 | 503 | 503 |
| Redis | 503 | 503 | Di luar skenario | Di luar skenario |

Liveness tetap `200`. Setelah dependency kembali, readiness pulih ke `200` dan ingestion baru mendapat `202` melalui proses API dan worker yang sama. Dalam kedua skenario, event dan replay yang diterima sebelum outage menghasilkan satu event tersimpan. Setelah satu kiriman baru, hasil akhir per skenario adalah dua event, enam reading, tiga alert pulih, serta nol backlog, pending, dan dead letter.

Unit test memeriksa kegagalan koneksi, shutdown PostgreSQL, timeout, Redis, dan antrean penuh. Tes integrasi memicu error schema saat autentikasi dan memverifikasi respons `500` tanpa header retry. `go test -count=1 -v ./...` lulus, termasuk 11 tes integrasi dengan PostgreSQL dan Redis nyata. `go vet ./...` dan validasi OpenAPI juga lulus.

- PostgreSQL — `artifacts/dependency-recovery/postgres-0cd877d0-2f8f-40f6-b5df-60f134371221/result.json`
- Redis — `artifacts/dependency-recovery/redis-718f8cdd-10b0-419d-89de-52c27e2afe33/result.json`

## Verifikasi integrasi TimescaleDB

Pada 20 November 2026, PostgreSQL aplikasi memakai ekstensi TimescaleDB 2.32.1 dengan migrasi 000006. Pemeriksaan server aktif menemukan hypertable `sensor_readings` dengan dua chunk dan 306 reading yang dipertahankan. Jumlah baris dan checksum isi 10 tabel domain sama sebelum dan sesudah migrasi. Volume PostgreSQL lama disimpan sebagai cadangan.

Tes awal menemukan deadlock ketika worker membentuk chunk pertama sambil memasukkan event yang sama. Writer kemudian mengambil lock tabel referensi sebelum insert event jika batch memerlukan chunk baru. Tes replay konkuren lulus 15 kali berturut-turut, dan suite berikutnya meluluskan 11 tes integrasi serta unit test. Suite dengan race detector lulus melalui Docker Linux. Pengujian browser terhadap stack utama memverifikasi heartbeat, replay, tiga alert, dan agregasi menit serta jam. Hasil k6 sebelumnya tetap menggambarkan konfigurasi saat masing-masing run dilakukan.

Bukti verifikasi ada di `artifacts/timescale-upgrade/20261120/active-postgres.txt`, `pre-upgrade-checksums.txt`, `post-upgrade-checksums.txt`, `concurrency-after.log`, `go-test-final.log`, `go-race.log`, dan `browser-smoke.log`.

