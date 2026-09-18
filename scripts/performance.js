import http from 'k6/http';
import { check } from 'k6';
import exec from 'k6/execution';

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
  thresholds: {
    'checks{scenario:measured}': ['rate==1'],
    'http_req_failed{scenario:measured}': ['rate==0'],
    'http_req_duration{scenario:measured}': ['p(95)<10'],
    'dropped_iterations{scenario:measured}': ['count==0'],
  },
};

const base = __ENV.BASE_URL || 'http://127.0.0.1:8080';
const paths = [
  '/api/v1/sentences/random',
  '/api/v1/sentences/random?categories=short,long',
  '/api/v1/sentences/random?min_length=5&max_length=80',
];
export function readMix() {
  const path = paths[exec.scenario.iterationInTest % paths.length];
  const response = http.get(`${base}${path}`, { timeout: __ENV.REQUEST_TIMEOUT || '10s' });
  check(response, { 'valid read returns 200': (r) => r.status === 200 });
}

export default readMix;
