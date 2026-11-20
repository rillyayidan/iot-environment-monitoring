#!/usr/bin/env bash
# Menjalankan demo via Bash untuk registrasi perangkat, heartbeat, deduplikasi, dan alert CO2.
set -euo pipefail

cd "$(dirname "$0")/.."

fail() {
    printf '%s\n' "$*" >&2
    exit 1
}

for tool in curl docker jq uuidgen; do
    command -v "$tool" >/dev/null || fail "Perintah $tool diperlukan untuk demo."
done

binding=$(docker compose port api 8080) || fail 'Jalankan docker compose up --build --wait terlebih dulu.'
base="http://$binding"
token=$(docker compose config --format json | jq -er '.services.api.environment.ADMIN_TOKEN | select(length > 0)') ||
    fail 'ADMIN_TOKEN belum tersedia di konfigurasi Compose.'
admin_header="Authorization: Bearer $token"

request() {
    local method=$1 path=$2 expected=$3 response status
    shift 3
    response=$(curl -sS --max-time 5 --request "$method" --write-out '\n%{http_code}' "$base$path" "$@") ||
        fail "Gagal menghubungi $method $path."
    status=${response##*$'\n'}
    [ "$status" = "$expected" ] || fail "$method $path menghasilkan HTTP $status, diharapkan $expected."
    printf '%s\n' "${response%$'\n'*}"
}

admin_post() {
    request POST "$1" 201 -H "$admin_header" -H 'Content-Type: application/json' -d "$2"
}

new_event_id() {
    uuidgen | tr '[:upper:]' '[:lower:]'
}

reading() {
    jq -cn --arg event_id "$1" --arg recorded_at "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" \
        --arg sensor_id "$sensor_id" --argjson value "$2" \
        '{event_id: $event_id, recorded_at: $recorded_at, measurements: [{sensor_id: $sensor_id, metric: "co2", value: $value}]}'
}

enqueue() {
    request POST /api/v1/readings 202 -H "$device_header" -H 'Content-Type: application/json' -d "$1" >/dev/null
}

wait_for() {
    local path=$1 attempt response
    shift
    for ((attempt=1; attempt<=15; attempt++)); do
        response=$(request GET "$path" 200 -H "$admin_header") || return 1
        if jq -e "$@" <<< "$response" >/dev/null; then
            printf '%s\n' "$response"
            return 0
        fi
        sleep 1
    done
    fail "Hasil yang diharapkan belum muncul pada $path. Cek docker compose logs worker."
}

request GET /health/ready 200 >/dev/null
printf 'API siap di %s\n' "$base"

high_id=$(new_event_id)
device=$(admin_post /api/v1/devices "{\"name\":\"Demo Mentor ${high_id:0:8}\",\"location\":\"Lab\"}")
device_id=$(jq -er '.id' <<< "$device")
device_header="X-API-Key: $(jq -er '.api_key' <<< "$device")"
device_path="/api/v1/devices/$device_id"

sensor=$(admin_post "$device_path/sensors" '{"metric":"co2"}')
sensor_id=$(jq -er '.id' <<< "$sensor")
rule=$(admin_post "/api/v1/sensors/$sensor_id/alert-rules" \
    '{"operator":"gt","threshold":1000,"recovery_threshold":900,"severity":"warning"}')
rule_id=$(jq -er '.id' <<< "$rule")
printf 'Perangkat %s dan sensor CO2 %s terdaftar\n' "$device_id" "$sensor_id"

before=$(request GET "$device_path/status" 200 -H "$admin_header" | jq -er '.connectivity')
request POST /api/v1/devices/heartbeat 204 -H "$device_header" >/dev/null
after=$(request GET "$device_path/status" 200 -H "$admin_header" | jq -er '.connectivity')
[ "$before" = NEVER_SEEN ] && [ "$after" = ONLINE ] || fail "Konektivitas tak terduga ($before -> $after)."
printf 'Heartbeat HTTP 204, %s -> %s\n' "$before" "$after"

high=$(reading "$high_id" 1200)
enqueue "$high"
enqueue "$high"
printf 'Dua kiriman identik 1200 ppm sama-sama menerima HTTP 202\n'

history="$device_path/readings?sensor_id=$sensor_id"
wait_for "$history" --arg event "$high_id" 'length == 1 and .[0].event_id == $event and .[0].value == 1200' >/dev/null
active=$(wait_for '/api/v1/alerts?status=ACTIVE&limit=200' --arg rule "$rule_id" \
    '[.[] | select(.rule_id == $rule)] | length == 1')
alert_id=$(jq -er --arg rule "$rule_id" '.[] | select(.rule_id == $rule) | .id' <<< "$active")
printf 'Satu reading 1200 ppm tersimpan, alert %s ACTIVE\n' "$alert_id"

# Jaga recorded_at event kedua lebih baru pada macOS, dengan presisi detik dari date.
sleep 1
low_id=$(new_event_id)
low=$(reading "$low_id" 850)
enqueue "$low"
wait_for "/api/v1/alerts/$alert_id" '.status == "RESOLVED"' >/dev/null
wait_for "$history" --arg high "$high_id" --arg low "$low_id" \
    'length == 2 and any(.[]; .event_id == $high and .value == 1200) and any(.[]; .event_id == $low and .value == 850)' >/dev/null
printf 'Dua reading unik tersimpan. Alert yang sama %s RESOLVED setelah 850 ppm.\n' "$alert_id"
