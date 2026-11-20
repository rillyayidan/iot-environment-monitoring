// Mendaftarkan perangkat, API key, sensor, dan aturan alert sebelum pengukuran k6.
import http from 'k6/http';

const count = Number(__ENV.FLEET_DEVICES);
const runID = __ENV.FLEET_RUN_ID;
const base = 'http://api:8080/api/v1';
const params = {
  headers: { Authorization: 'Bearer ' + __ENV.ADMIN_TOKEN, 'Content-Type': 'application/json' },
  timeout: '15s',
};
const sensors = [
  { metric: 'co2', threshold: 1000, recovery: 900, severity: 'critical' },
  { metric: 'temperature', threshold: 30, recovery: 28, severity: 'warning' },
  { metric: 'humidity', threshold: 70, recovery: 65, severity: 'warning' },
];

export const options = { vus: 1, iterations: 1, batch: 36, batchPerHost: 36, setupTimeout: '5m' };

function post(path, body) {
  return ['POST', base + path, JSON.stringify(body), params];
}

function requireCreated(response, kind) {
  if (response.status !== 201) {
    throw new Error(kind + ' registration returned HTTP ' + response.status);
  }
  return response.json();
}

export function setup() {
  if (!Number.isInteger(count) || count < 1 || !runID || !__ENV.ADMIN_TOKEN) {
    throw new Error('fleet size, run ID and admin token are required');
  }
  for (let first = 0; first < count; first += 12) {
    const names = [];
    for (let index = first; index < Math.min(count, first + 12); index++) {
      const profile = index % 5 === 0 ? 'active' : index % 5 === 1 ? 'recover' : 'normal';
      names.push({ index, profile, name: ['fleet', runID, index, profile].join('_') });
    }
    const devices = http.batch(names.map((item) => post('/devices', {
      name: item.name, location: 'Synthetic environmental monitoring site',
    }))).map((response) => requireCreated(response, 'device'));
    const registered = http.batch(devices.flatMap((device) => sensors.map((sensor) => post(
      '/devices/' + device.id + '/sensors', { metric: sensor.metric },
    )))).map((response) => requireCreated(response, 'sensor'));
    const rules = http.batch(registered.map((sensor, index) => {
      const policy = sensors[index % sensors.length];
      return post('/sensors/' + sensor.id + '/alert-rules', {
        operator: 'gte', threshold: policy.threshold, recovery_threshold: policy.recovery,
        severity: policy.severity,
      });
    })).map((response) => requireCreated(response, 'alert rule'));

    for (let i = 0; i < devices.length; i++) {
      const from = i * sensors.length;
      console.log(JSON.stringify({
        index: names[i].index,
        profile: names[i].profile,
        id: devices[i].id,
        apiKey: devices[i].api_key,
        sensors: registered.slice(from, from + sensors.length).map((sensor) => sensor.id),
        rules: rules.slice(from, from + sensors.length).map((rule) => rule.id),
      }));
    }
  }
}

export default function () {}

export function handleSummary() {
  return {};
}
