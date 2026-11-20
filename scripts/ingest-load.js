// Mengirim pembacaan dan replay dari banyak VU yang memakai satu perangkat dan sensor CO2.
import http from 'k6/http';
import exec from 'k6/execution';
import { check, sleep } from 'k6';
import { Counter } from 'k6/metrics';

const suppliedVUs = Number(__ENV.LOAD_VUS || 100);
const targetRate = Number(__ENV.LOAD_RATE || 0);
const duration = __ENV.LOAD_DURATION || '30s';
if (![100, 500, 1000, 2000].includes(suppliedVUs)) {
  throw new Error('LOAD_VUS must be 100, 500, 1000, or 2000');
}
if (!__ENV.LOAD_DEVICE_KEY || !__ENV.LOAD_SENSOR_ID || !__ENV.LOAD_RUN_ID) {
  throw new Error('set LOAD_DEVICE_KEY, LOAD_SENSOR_ID, and LOAD_RUN_ID or use scripts/load-test.ps1');
}

export const options = {
  scenarios: {
    ingestion: targetRate > 0 ? {
      executor: 'constant-arrival-rate',
      preAllocatedVUs: suppliedVUs,
      rate: targetRate,
      timeUnit: '1s',
      duration,
      gracefulStop: '10s',
    } : {
      executor: 'constant-vus',
      vus: suppliedVUs,
      duration,
      gracefulStop: '10s',
    },
  },
  summaryTrendStats: ['avg', 'med', 'p(95)', 'p(99)', 'max'],
};

const queuedEvents = new Counter('queued_unique_events');
const queuedRequests = new Counter('queued_requests');
const retryRequests = new Counter('duplicate_attempts');
const confirmedReplays = new Counter('confirmed_replays');
const unavailableRequests = new Counter('status_503');
const otherFailures = new Counter('other_failures');
const params = {
  headers: { 'Content-Type': 'application/json', 'X-API-Key': __ENV.LOAD_DEVICE_KEY },
  timeout: '8s',
};
const base = __ENV.LOAD_BASE_URL || 'http://api:8080';
const prefix = __ENV.LOAD_RUN_ID.slice(0, 24);

function send(body, label) {
  const response = http.post(base + '/api/v1/readings', body, params);
  unavailableRequests.add(response.status === 503 ? 1 : 0);
  otherFailures.add(response.status !== 202 && response.status !== 503 ? 1 : 0);
  check(response, { [label]: (r) => r.status === 202 });
  return response.status === 202 ? 1 : 0;
}

export default function () {
  const iteration = exec.scenario.iterationInTest;
  const suffix = iteration.toString(16).padStart(12, '0');
  const body = JSON.stringify({
    event_id: prefix + suffix,
    recorded_at: new Date().toISOString(),
    measurements: [{ sensor_id: __ENV.LOAD_SENSOR_ID, metric: 'co2', value: 500 + (iteration % 700) }],
  });
  let successes = send(body, 'first request queued');

  const retry = iteration % 10 === 0;
  retryRequests.add(retry ? 1 : 0);
  if (retry) {
    successes += send(body, 'duplicate request queued');
  }
  confirmedReplays.add(successes === 2 ? 1 : 0);
  queuedRequests.add(successes);
  queuedEvents.add(successes > 0 ? 1 : 0);
  if (targetRate === 0) {
    sleep(0.25);
  }
}

export function handleSummary(data) {
  return { '/out/k6.json': JSON.stringify(data, null, 2) };
}

