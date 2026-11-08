"""使用本地样例比较 oracle 文件调用与预热管道往返；不访问平台。

python -m booking_tools.benchmark_oracle --samples evidence/captcha-samples/2026-11-08 \
    --output runtime/oracle-benchmark.json
"""
import argparse
import json
import math
import statistics
import subprocess
import sys
import tempfile
import time
from datetime import datetime, timedelta, timezone
from pathlib import Path

from .session import ROOT, write_json


def distribution(values):
    ordered = sorted(values)
    return {'n': len(values), 'medianMs': round(statistics.median(ordered), 3),
            'p95Ms': round(ordered[math.ceil(.95 * len(ordered)) - 1], 3),
            'minMs': round(ordered[0], 3), 'maxMs': round(ordered[-1], 3)}


def benchmark(samples, repeats, mode):
    command = [sys.executable, '-X', 'utf8', str(ROOT / 'main.py'), 'oracle-' + mode]
    def request(index):
        return {'account': 'offline-benchmark',
                'deadline': (datetime.now(timezone.utc) + timedelta(minutes=1)).isoformat(),
                'challenge': dict(samples[index % len(samples)], token='offline-' + str(index))}
    def verify(reply, index):
        if reply.get('token') != 'offline-' + str(index) or type(reply.get('x')) is not int:
            raise ValueError('invalid benchmark reply')
    cold, warm = [], []
    with tempfile.TemporaryDirectory(prefix='oracle-benchmark-') as directory:
        path = Path(directory) / 'request.json'
        for index in range(repeats):
            write_json(path, request(index))
            started = time.perf_counter()
            subprocess.run(command + ['--request', str(path)], capture_output=True, check=True, timeout=15)
            reply = json.loads(Path(str(path) + '.reply').read_text(encoding='utf-8'))
            cold.append((time.perf_counter() - started) * 1000)
            verify(reply, index)
    started = time.perf_counter()
    worker = subprocess.Popen(command + ['--stdio'], stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                              stderr=subprocess.DEVNULL, text=True, encoding='utf-8')
    try:
        if json.loads(worker.stdout.readline()) != {'ready': True}:
            raise ValueError('worker did not become ready')
        startup_ms = (time.perf_counter() - started) * 1000
        for index in range(repeats):
            payload = request(index)
            started = time.perf_counter()
            worker.stdin.write(json.dumps(payload) + '\n')
            worker.stdin.flush()
            reply = json.loads(worker.stdout.readline())
            warm.append((time.perf_counter() - started) * 1000)
            verify(reply, index)
        worker.stdin.close()
        if worker.wait(timeout=5) != 0:
            raise ValueError('worker failed to exit')
    finally:
        if worker.poll() is None:
            worker.kill()
        worker.wait(timeout=5)
        worker.stdin.close()
        worker.stdout.close()
    return {'at': datetime.now(timezone.utc).isoformat(), 'samples': len(samples), 'mode': mode,
            'networkRequests': 0, 'orders': 0, 'fileProcess': distribution(cold),
            'stdioStartupMs': round(startup_ms, 3), 'stdioFirstReplyMs': round(warm[0], 3),
            'stdioRoundTrip': distribution(warm),
            'note': 'Local samples only. Startup is measured separately; file timing excludes request creation.'}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--samples', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--repeats', type=int, default=20)
    parser.add_argument('--mode', choices=['random', 'reference'], default='reference')
    args = parser.parse_args()
    samples = [json.loads(p.read_text(encoding='utf-8'))['challenge']
               for p in sorted(args.samples.glob('challenge-*.json'))]
    if not samples or args.repeats < 1:
        parser.error('need local samples and a positive repeat count')
    report = benchmark(samples, args.repeats, args.mode)
    write_json(args.output, report)
    print(json.dumps(report, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
