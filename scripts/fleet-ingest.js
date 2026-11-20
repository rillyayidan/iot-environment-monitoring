// Mengirim pembacaan tiga sensor dan replay dari banyak perangkat secara konkuren lewat k6.
import http from 'k6/http';
import exec from 'k6/execution';
import { SharedArray } from 'k6/data';
import { sleep } from 'k6';
import { Counter } from 'k6/metrics';

const fleet = new SharedArray('provisioned fleet', () =>
  open('/tmp/fleet-secrets.ndjson').trim().split('\n').map((line) => JSON.parse(line)));
const count = Number(__ENV.FLEET_DEVICES);
if (fleet.length !== count || !__ENV.FLEET_RUN_ID) {
  throw new Error('fixture count or run ID is invalid');
}

export const options = {
  discardResponseBodies: true,
  scenarios: {
    telemetry: { executor: 'constant-vus', vus: count, duration: __ENV.FLEET_DURATION || '60s', gracefulStop: '10s' },
  },
  summaryTrendStats: ['avg', 'med', 'p(95)', 'p(99)', 'max'],
};
http.setResponseCallback(http.expectedStatuses(202));

const metrics = ['co2', 'temperature', 'humidity'];
const accepted = new Counter('accepted_unique_events');
const acceptedRequests = new Counter('accepted_requests');
const attempts = new Counter('replay_attempts');
const successfulReplays = new Counter('confirmed_replays');
const unavailable = new Counter('status_503');
const transportErrors = new Counter('transport_errors');
const prefix = __ENV.FLEET_RUN_ID.slice(0, 24);
const readingsURL = 'http://api:8080/api/v1/readings';
let device;
let params;
let interval;

function clamp(value) {
  return Math.max(0, Math.min(1, value));
}

function generateReadings(profile, elapsed, index, iteration) {
  let elevation = 0;
  if (profile === 'active') {
    elevation = clamp((elapsed - 27) / 10);
  } else if (profile === 'recover') {
    elevation = Math.min(clamp((elapsed - 8) / 8), clamp((35 - elapsed) / 7));
  }
  const oscillation = Math.sin(elapsed / 4 + index * 0.61);
  const noise = Math.sin(index * 1.41 + iteration * 1.87);
  return [
    Math.round(650 + oscillation * 70 + noise * 20 + elevation * 760),
    Number((23.7 + oscillation * 1.1 + noise * 0.25 + elevation * 10.1).toFixed(1)),
    Number((51 + oscillation * 4 + noise * 1.6 + elevation * 29).toFixed(1)),
  ];
}

function send(body) {
  const res = http.post(readingsURL, body, params);
  unavailable.add(res.status === 503 ? 1 : 0);
  transportErrors.add(res.status === 0 ? 1 : 0);
  return res.status === 202 ? 1 : 0;
}

export default function () {
  if (!device) {
    device = fleet[exec.vu.idInTest - 1];
    if (!device) throw new Error('VU has no provisioned device');
    params = {
      headers: { 'Content-Type': 'application/json', 'X-API-Key': device.apiKey },
      timeout: '8s',
    };
    interval = 0.8 + ((device.index * 37) % 401) / 1000;
    sleep(((device.index * 71) % 997) / 1000);
  }

  const now = Date.now();
  const elapsed = (now - exec.scenario.startTime) / 1000;
  const iteration = exec.vu.iterationInScenario;
  const event = prefix + exec.scenario.iterationInTest.toString(16).padStart(12, '0');
  const recordedAt = new Date(now).toISOString();
  const values = generateReadings(device.profile, elapsed, device.index, iteration);
  const body = JSON.stringify({
    event_id: event,
    recorded_at: recordedAt,
    measurements: device.sensors.map((sensor, i) => ({ sensor_id: sensor, metric: metrics[i], value: values[i] })),
  });
  let successes = send(body);
  const replay = iteration % 10 === 9;
  attempts.add(replay ? 1 : 0);
  if (replay) successes += send(body);
  successfulReplays.add(successes === 2 ? 1 : 0);
  acceptedRequests.add(successes);
  accepted.add(successes > 0 ? 1 : 0);
  if (successes > 0) console.log([device.id, event, recordedAt, ...values].join(','));
  const remaining = interval - (Date.now() - now) / 1000;
  if (remaining > 0) sleep(remaining);
}

export function handleSummary(data) {
  const manifest = fleet.map(({ index, profile, id, sensors, rules }) => ({ index, profile, id, sensors, rules }));
  return {
    '/tmp/k6.json': JSON.stringify(data, null, 2),
    '/tmp/devices.json': JSON.stringify(manifest, null, 2),
  };
}
