import http from 'k6/http';
import { check, sleep } from 'k6';

// k6 Options configuration
export const options = {
  scenarios: {
    constant_load: {
      executor: 'constant-vus',
      vus: 300,
      duration: '30s',
    },
  },
  thresholds: {
    // 95% of requests must complete in less than 15ms.
    // Local in-memory evaluations are sub-millisecond, so latency is mostly network.
    http_req_duration: ['p(95)<15'],
    // Less than 1% of requests should fail.
    http_req_failed: ['rate<0.01'],
  },
};

export default function () {
  const targetUrl = __ENV.TARGET_URL || 'http://localhost:8085/evaluate';
  
  // Hit the evaluation endpoint
  const res = http.get(targetUrl);
  
  // Check the response status and content
  check(res, {
    'status is 200': (r) => r.status === 200,
    'has value': (r) => r.body.includes('"value"'),
  });
}
