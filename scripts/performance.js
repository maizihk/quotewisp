import http from 'k6/http';
import { check } from 'k6';
import exec from 'k6/execution';

const thresholds = {
  'checks{scenario:measured}': ['rate==1'],
  'http_req_failed{scenario:measured}': ['rate==0'],
  'http_req_duration{scenario:measured}': ['p(95)<10'],
  'dropped_iterations{scenario:measured}': ['count==0'],
};
if (__ENV.MIN_REQUESTS) {
  thresholds['http_reqs{scenario:measured}'] = [`count>=${Number(__ENV.MIN_REQUESTS)}`];
}

// Optional measured-phase interval covering externally scheduled imports.
// Bounds are relative to the start of measured traffic, not warmup.
const importStart = Number(__ENV.IMPORT_WINDOW_START || -1);
const importEnd = Number(__ENV.IMPORT_WINDOW_END || -1);
if (importStart >= 0 && importEnd > importStart) {
  thresholds['http_req_duration{scenario:measured,window:import}'] = ['p(95)<10'];
  thresholds['http_req_failed{scenario:measured,window:import}'] = ['rate==0'];
  thresholds['http_reqs{scenario:measured,window:import}'] = ['count>0'];
  thresholds['http_req_duration{scenario:measured,window:steady}'] = ['p(95)<10'];
}

export const options = {
  scenarios: {
    warmup: {
      executor: 'constant-arrival-rate', rate: Number(__ENV.WARMUP_RPS || 100),
      timeUnit: '1s', duration: __ENV.WARMUP_DURATION || '60s',
      preAllocatedVUs: Number(__ENV.PREALLOCATED_VUS || 32), maxVUs: Number(__ENV.MAX_VUS || 256),
      exec: 'readMix', tags: { phase: 'warmup' },
    },
    measured: {
      executor: 'constant-arrival-rate', rate: Number(__ENV.RPS || 1000),
      timeUnit: '1s', duration: __ENV.DURATION || '10m', startTime: __ENV.WARMUP_DURATION || '60s',
      preAllocatedVUs: Number(__ENV.PREALLOCATED_VUS || 32), maxVUs: Number(__ENV.MAX_VUS || 256),
      exec: 'readMix', tags: { phase: __ENV.PHASE || 'steady' },
    },
  },
  thresholds,
};

const base = __ENV.BASE_URL || 'http://127.0.0.1:8080';
const paths = [
  '/api/v1',
  '/api/v1?categories=short,long',
  '/api/v1?min_length=5&max_length=80',
];
export function readMix() {
  const path = paths[exec.scenario.iterationInTest % paths.length];
  const elapsed = (Date.now() - exec.scenario.startTime) / 1000;
  const window = exec.scenario.name === 'measured' && elapsed >= importStart && elapsed < importEnd ? 'import' : 'steady';
  const response = http.get(`${base}${path}`, {
    timeout: __ENV.REQUEST_TIMEOUT || '10s', tags: { window },
  });
  check(response, { 'valid read returns 200': (r) => r.status === 200 });
}

export default readMix;
