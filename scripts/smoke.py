#!/usr/bin/env python3
"""Exercise the live stack without logging the API token or touching old data."""
from datetime import datetime, timezone
from pathlib import Path
import json
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request

project = Path(__file__).resolve().parents[1]
settings = dict(line.split('=', 1) for line in (project / '.env').read_text().splitlines() if '=' in line)
base = f"http://localhost:{settings.get('HTTP_PORT', '8080')}"
token = settings['NETWATCH_API_TOKEN']

def request(path, method='GET', body=None, authenticated=True):
    headers = {'Content-Type': 'application/json'}
    if authenticated:
        headers['Authorization'] = f'Bearer {token}'
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(base + path, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req, timeout=10) as response:
            return response.status, json.load(response)
    except urllib.error.HTTPError as error:
        return error.code, json.load(error)

def wait_for(check, seconds, message):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        result = check()
        if result:
            return result
        time.sleep(1)
    raise AssertionError(message)

assert request('/health/ready')[0] == 200
assert request('/api/devices', authenticated=False)[0] == 401
assert request('/api/login', 'POST', {'token': 'incorrect'}, False)[0] == 401
assert request('/api/telemetry', 'POST', {'device_id': 'incomplete'})[0] == 400
print('Readiness, authentication, and input validation passed.')

device = f'smoke-test-{time.time_ns()}'
event_ids = []
for cpu in (95, 45):
    payload = {
        'device_id': device, 'source': 'simulated',
        'timestamp': datetime.now(timezone.utc).isoformat(),
        'cpu_percent': cpu, 'memory_percent': 40, 'packet_loss_pct': 0,
    }
    code, data = request('/api/telemetry', 'POST', payload)
    assert code == 202
    event_ids.append(data['event_id'])

wait_for(lambda: len(request(f'/api/devices/{device}/history')[1]) == 2, 30, 'Telemetry did not reach PostgreSQL')
def find_incident():
    return next((item for item in request('/api/incidents?limit=500')[1]
                 if item['device_id'] == device and item['source_event_id'] == event_ids[0]), None)
incident = wait_for(find_incident, 30, 'High-CPU incident was not persisted')
assert incident['source'] == 'simulated'
encoded = urllib.parse.quote(incident['id'], safe='')
assert request(f'/api/incidents/{encoded}', 'PATCH', {'status': 'resolved'})[0] == 200
assert find_incident()['status'] == 'resolved'
assert request(f'/api/incidents/{encoded}', 'PATCH', {'status': 'open'})[0] == 200
print('Telemetry ingestion, PostgreSQL history, incident detection, and status updates passed.')

subprocess.run([str(project / 'scripts/collect-linux.sh'), '-device', 'wsl-host', '-count', '1'], cwd=project, check=True)
actual = wait_for(lambda: next((item for item in request('/api/devices')[1]
                              if item['device_id'] == 'wsl-host' and item['source'] == 'linux'), None),
                  30, 'Real Linux readings did not reach the dashboard API')
assert all(0 <= actual[name] <= 100 for name in ('cpu_percent', 'memory_percent', 'packet_loss_pct'))
print(f"Live Linux collection passed: CPU={actual['cpu_percent']:.1f}%, memory={actual['memory_percent']:.1f}%, measured ICMP loss={actual['packet_loss_pct']:.1f}%.")

if request('/api/config')[1]['ai_enabled']:
    def completed_analysis():
        item = find_incident()
        if item['analysis_status'] == 'error':
            raise AssertionError(f"AI failed: {item['analysis_error']}")
        return item if item['analysis_status'] == 'complete' else None
    analyzed = wait_for(completed_analysis, 180, 'Local AI did not finish analysis')
    assert analyzed['analysis']['summary'] and analyzed['analysis']['suggested_checks']
    assert analyzed['analysis']['evidence']
    print(f"Actual local AI analysis passed with model {analyzed['analysis']['model']}.")
print('Live smoke test passed. Test readings are retained and explicitly labeled simulated.')
