"use strict";

const metrics = {
    co2: { label: "CO₂", unit: "ppm", threshold: 1000, recovery: 900, high: 1200, low: 850, padding: 100 },
    temperature: { label: "Suhu", unit: "°C", threshold: 26, recovery: 25, high: 31.5, low: 25, padding: 1 },
    humidity: { label: "Kelembapan", unit: "% RH", threshold: 60, recovery: 55, high: 74, low: 55, padding: 5 },
};
const state = {
    token: "", device: null, sensors: {}, rules: {}, connectivity: null,
    readings: [], buckets: [], alerts: [], lastEvent: null, lastResponse: null,
    metric: "co2", historyMode: "readings", busy: false,
};
const el = (id) => document.getElementById(id);
const metricKeys = Object.keys(metrics);
const clock = new Intl.DateTimeFormat("id-ID", { hour: "2-digit", minute: "2-digit", second: "2-digit" });
const dateTime = new Intl.DateTimeFormat("id-ID", { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", second: "2-digit" });
const number = new Intl.NumberFormat("id-ID", { maximumFractionDigits: 2 });
const formatTime = (value) => dateTime.format(new Date(value));
const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const configured = () => metricKeys.every((metric) => state.rules[metric]);

async function request(path, { method = "GET", token = state.token, deviceKey, body } = {}) {
    const headers = { Accept: "application/json" };
    if (deviceKey) headers["X-API-Key"] = deviceKey;
    else if (token) headers.Authorization = `Bearer ${token}`;
    if (body !== undefined) headers["Content-Type"] = "application/json";

    const response = await fetch(path, {
        method,
        headers,
        body: body === undefined ? undefined : JSON.stringify(body),
        signal: AbortSignal.timeout(10000),
    });
    const data = response.status === 204 ? null : await response.json().catch(() => null);
    if (!response.ok) throw new Error(`HTTP ${response.status} · ${data?.error ?? "Permintaan gagal"}`);
    return { status: response.status, data };
}

async function checkHealth() {
    try {
        const { data } = await request("/health/ready", { token: "" });
        if (data?.status !== "ready") throw new Error("API belum siap");
        el("api-status").dataset.state = "ready";
        el("api-text").textContent = "API siap";
    } catch {
        el("api-status").dataset.state = "error";
        el("api-text").textContent = "API belum siap";
    }
}

function showNotice(message) {
    const element = el("notice");
    element.textContent = message;
    element.hidden = !message;
}

function showEvent(label, details, id = "") {
    const badge = el("event-result");
    badge.textContent = label;
    badge.className = "badge info";
    el("event-text").textContent = details;
    el("event-id").textContent = id;
    el("event-id").hidden = !id;
}

function renderControls() {
    el("open-token").textContent = state.token ? "Token terhubung" : "Hubungkan token";
    el("open-token").disabled = state.busy;
    el("setup-form").hidden = configured();
    el("demo-actions").hidden = !configured();
    el("demo-name").disabled = Boolean(state.device);
    el("setup").textContent = state.device ? "Lanjutkan penyiapan sensor" : "Siapkan perangkat demo";
    for (const id of ["setup", "heartbeat", "high", "recover", "refresh", "history-mode"]) {
        el(id).disabled = state.busy;
    }
    el("replay").disabled = state.busy || !state.lastEvent;
}

function renderDevice() {
    if (!state.device) return;
    const status = state.connectivity?.connectivity ?? "NEVER_SEEN";
    el("device-name").textContent = state.device.name;
    el("device-location").textContent = state.device.location;
    el("device-location").title = state.device.id;
    el("connectivity").textContent = status;
    const tone = { ONLINE: "good", OFFLINE: "warning", NEVER_SEEN: "neutral" };
    el("connectivity").className = `badge ${tone[status] ?? "neutral"}`;
    el("last-seen").textContent = state.connectivity?.last_seen_at
        ? `Terakhir terlihat ${formatTime(state.connectivity.last_seen_at)}` : "";
}

function renderValues() {
    for (const metric of metricKeys) {
        const latest = state.readings.find((reading) => reading.metric === metric);
        el(`value-${metric}`).textContent = latest ? number.format(latest.value) : "—";
    }
}

function cellRow(tag, values) {
    const row = document.createElement("tr");
    for (const value of values) {
        const cell = document.createElement(tag);
        cell.textContent = value;
        row.append(cell);
    }
    return row;
}

function renderHistory() {
    const readings = state.historyMode === "readings";
    const columns = readings ? ["Waktu", "Sensor", "Nilai", "Event ID"]
        : ["Mulai bucket", "Sensor", "Rata-rata", "Min", "Maks", "Jumlah"];
    el("history-head").replaceChildren(cellRow("th", columns));
    const entries = readings ? state.readings : state.buckets;
    const rows = entries.map((row) => {
        const metric = metrics[row.metric];
        const measurement = (value) => `${number.format(value)} ${metric?.unit ?? row.unit}`;
        if (readings) {
            const tr = cellRow("td", [formatTime(row.recorded_at), metric?.label ?? row.metric, measurement(row.value), row.event_id.slice(0, 8)]);
            tr.lastElementChild.title = row.event_id;
            return tr;
        }
        return cellRow("td", [formatTime(row.bucket_start), metric?.label ?? row.metric, measurement(row.avg),
            measurement(row.min), measurement(row.max), number.format(row.count)]);
    });
    if (rows.length === 0) {
        const empty = cellRow("td", [readings ? "Belum ada pembacaan." : "Belum ada agregasi."]);
        empty.firstElementChild.colSpan = columns.length;
        rows.push(empty);
    }
    el("history-body").replaceChildren(...rows);
}

function renderAlerts() {
    const rows = [];
    for (const metric of metricKeys) {
        const alert = state.alerts.find((item) => item.rule_id === state.rules[metric]);
        if (!alert) continue;
        const item = document.createElement("li");
        const info = document.createElement("div");
        const title = document.createElement("strong");
        const value = document.createElement("small");
        const badge = document.createElement("span");
        title.textContent = metrics[metric].label;
        value.textContent = `Pemicu ${number.format(alert.triggered_value)} ${metrics[metric].unit}`;
        info.append(title, value);
        badge.className = `badge ${alert.status === "ACTIVE" ? "warning" : "good"}`;
        badge.textContent = alert.status;
        badge.title = alert.id;
        item.append(info, badge);
        rows.push(item);
    }
    if (rows.length === 0) {
        const empty = document.createElement("li");
        empty.className = "empty-alert";
        empty.textContent = "Belum ada peringatan.";
        rows.push(empty);
    }
    el("alerts").replaceChildren(...rows);
}

function drawChart() {
    const canvas = el("chart");
    const { width, height } = canvas.getBoundingClientRect();
    if (!width || !height) return;
    const scale = window.devicePixelRatio || 1;
    canvas.width = Math.round(width * scale);
    canvas.height = Math.round(height * scale);
    const ctx = canvas.getContext("2d");
    ctx.scale(scale, scale);
    const { label, threshold, padding, unit } = metrics[state.metric];
    const rows = state.readings.filter(({ metric }) => metric === state.metric).reverse();
    const values = rows.map(({ value }) => value);
    const low = Math.min(threshold, ...values) - padding;
    const high = Math.max(threshold, ...values) + padding;
    const area = { left: 51, right: width - 15, top: 17, bottom: height - 27 };
    const y = (value) => area.bottom - (value - low) / (high - low) * (area.bottom - area.top);
    const times = rows.map(({ recorded_at }) => Date.parse(recorded_at));
    const start = times[0];
    const span = times.at(-1) - start;
    const x = (time) => span ? area.left + (time - start) / span * (area.right - area.left) : (area.left + area.right) / 2;

    canvas.setAttribute("aria-label", `Grafik ${label}, ${rows.length} pembacaan dalam ${unit}`);
    el("chart-empty").hidden = rows.length > 0;
    ctx.font = "13px Segoe UI, system-ui, sans-serif";
    ctx.fillStyle = "#53675c";
    ctx.textAlign = "right";
    ctx.textBaseline = "middle";
    for (let i = 0; i <= 4; i++) {
        const value = low + (high - low) * i / 4;
        const line = y(value);
        ctx.beginPath();
        ctx.strokeStyle = "#e0e8e2";
        ctx.moveTo(area.left, line);
        ctx.lineTo(area.right, line);
        ctx.stroke();
        ctx.fillText(number.format(value), area.left - 8, line, area.left - 10);
    }

    ctx.beginPath();
    ctx.setLineDash([5, 5]);
    ctx.strokeStyle = "#9b582c";
    ctx.moveTo(area.left, y(threshold));
    ctx.lineTo(area.right, y(threshold));
    ctx.stroke();
    ctx.setLineDash([]);
    if (rows.length === 0) return;

    ctx.beginPath();
    ctx.lineWidth = 2.5;
    ctx.strokeStyle = "#14654f";
    rows.forEach(({ value }, index) => {
        if (index === 0) ctx.moveTo(x(times[index]), y(value));
        else ctx.lineTo(x(times[index]), y(value));
    });
    ctx.stroke();
    ctx.fillStyle = "#14654f";
    rows.forEach(({ value }, index) => {
        ctx.beginPath();
        ctx.arc(x(times[index]), y(value), 3.5, 0, Math.PI * 2);
        ctx.fill();
    });

    ctx.fillStyle = "#53675c";
    ctx.textAlign = span ? "left" : "center";
    ctx.fillText(clock.format(start), x(start), height - 8);
    if (span) {
        ctx.textAlign = "right";
        ctx.fillText(clock.format(start + span), area.right, height - 8);
    }
}

async function refresh() {
    if (!state.device) return;
    const devicePath = `/api/v1/devices/${state.device.id}`;
    const [status, readings, alerts, aggregates] = await Promise.all([
        request(`${devicePath}/status`),
        request(`${devicePath}/readings?limit=200`),
        request("/api/v1/alerts?limit=200"),
        state.historyMode === "readings" ? null :
            request(`${devicePath}/aggregations?bucket=${state.historyMode}&limit=200`),
    ]);
    state.connectivity = status.data;
    state.readings = readings.data ?? [];
    state.alerts = (alerts.data ?? []).filter(({ device_id }) => device_id === state.device.id);
    state.buckets = aggregates?.data ?? [];
    renderDevice();
    renderValues();
    renderHistory();
    renderAlerts();
    drawChart();
}

function askForToken() {
    el("token-error").hidden = true;
    if (!el("token-dialog").open) el("token-dialog").showModal();
    el("token").focus();
}

async function run(action) {
    if (state.busy) return;
    if (!state.token) {
        askForToken();
        return;
    }
    state.busy = true;
    renderControls();
    showNotice("");
    try {
        await action();
    } catch (error) {
        showNotice(error.message);
    } finally {
        state.busy = false;
        renderControls();
    }
}

async function setup() {
    if (!state.device) {
        const name = el("demo-name").value.trim();
        if (!name) throw new Error("Masukkan nama perangkat.");
        const result = await request("/api/v1/devices", {
            method: "POST", body: { name, location: "Lab demo" },
        });
        state.device = result.data;
        renderDevice();
    }
    for (const [metric, { threshold, recovery }] of Object.entries(metrics)) {
        if (!state.sensors[metric]) {
            const sensor = await request(`/api/v1/devices/${state.device.id}/sensors`, {
                method: "POST", body: { metric },
            });
            state.sensors[metric] = sensor.data.id;
        }
        if (!state.rules[metric]) {
            const rule = await request(`/api/v1/sensors/${state.sensors[metric]}/alert-rules`, {
                method: "POST",
                body: { operator: "gt", threshold, recovery_threshold: recovery, severity: "warning" },
            });
            state.rules[metric] = rule.data.id;
        }
    }
    await refresh();
}

async function sendReading(type) {
    const replay = type === "replay";
    const activeBefore = state.alerts.filter(({ status }) => status === "ACTIVE");
    const body = replay ? state.lastEvent : {
        event_id: crypto.randomUUID(),
        recorded_at: new Date().toISOString(),
        measurements: metricKeys.map((metric) => ({
            sensor_id: state.sensors[metric], metric, value: metrics[metric][type],
        })),
    };
    const { status } = await request("/api/v1/readings", {
        method: "POST", deviceKey: state.device.api_key, body,
    });
    state.lastEvent = body;
    showEvent(`HTTP ${status}`, replay ? "Payload identik dikirim ulang ke antrean." :
        "Redis menerima event. Menunggu pembacaan muncul di histori.", body.event_id);
    if (replay) {
        await refresh();
        const count = state.readings.filter(({ event_id }) => event_id === body.event_id).length;
        showEvent(`HTTP ${status}`, `Replay masuk antrean. Histori saat dicek menampilkan ${count} reading untuk event ini.`, body.event_id);
        return;
    }

    const deadline = Date.now() + 12000;
    do {
        await refresh();
        const count = state.readings.filter(({ event_id }) => event_id === body.event_id).length;
        const alertsUpdated = type === "high"
            ? metricKeys.every((metric) => state.alerts.some((item) =>
                item.rule_id === state.rules[metric] && item.status === "ACTIVE"))
            : activeBefore.every(({ id }) => state.alerts.some((item) =>
                item.id === id && item.status === "RESOLVED"));
        if (count === body.measurements.length && alertsUpdated) {
            showEvent(`HTTP ${status}`, `${count} reading sudah tersimpan di PostgreSQL.`, body.event_id);
            return;
        }
        await delay(300);
    } while (Date.now() < deadline);
    showEvent(`HTTP ${status}`, "Event sudah diterima. Tekan Segarkan untuk memeriksa histori dan alert lagi.", body.event_id);
}

el("open-token").addEventListener("click", askForToken);
el("token-cancel").addEventListener("click", () => el("token-dialog").close());
el("token-dialog").addEventListener("close", () => { el("token").value = ""; });
el("token-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const input = el("token");
    const token = input.value.trim();
    const errorLabel = el("token-error");
    el("token-connect").disabled = true;
    errorLabel.hidden = true;
    try {
        if (!token) throw new Error("Masukkan token operator.");
        await request("/api/v1/devices?limit=1", { token });
        state.token = token;
        el("token-dialog").close();
        showNotice("");
        renderControls();
        if (state.device) await run(refresh);
    } catch (error) {
        errorLabel.textContent = error.message;
        errorLabel.hidden = false;
    } finally {
        el("token-connect").disabled = false;
    }
});

el("setup-form").addEventListener("submit", (event) => {
    event.preventDefault();
    run(setup);
});
el("heartbeat").addEventListener("click", () => run(async () => {
    await request("/api/v1/devices/heartbeat", { method: "POST", deviceKey: state.device.api_key });
    await refresh();
}));
el("high").addEventListener("click", () => run(() => sendReading("high")));
el("replay").addEventListener("click", () => run(() => sendReading("replay")));
el("recover").addEventListener("click", () => run(() => sendReading("low")));
el("refresh").addEventListener("click", () => run(refresh));
el("history-mode").addEventListener("change", () => {
    state.historyMode = el("history-mode").value;
    state.buckets = [];
    renderHistory();
    if (state.device) run(refresh);
});

for (const button of document.querySelectorAll("[data-metric]")) {
    button.addEventListener("click", () => {
        state.metric = button.dataset.metric;
        for (const candidate of document.querySelectorAll("[data-metric]")) {
            candidate.setAttribute("aria-pressed", String(candidate === button));
        }
        drawChart();
    });
}

new ResizeObserver(drawChart).observe(el("chart"));
for (const [metric, { threshold, unit }] of Object.entries(metrics)) {
    el(`threshold-${metric}`).textContent = `> ${number.format(threshold)} ${unit}`;
}
renderControls();
renderHistory();
checkHealth();
setInterval(checkHealth, 12000);
