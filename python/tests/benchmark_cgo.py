"""cgo bridge overhead measurement.

Measures the full Python -> ctypes -> Go round-trip of the cheapest exported
operation (``GetVersion``), which is pure overhead: no cloud I/O happens, so
the elapsed time is the cgo + JSON boundary cost.

Run with:
    OPENCLD_SO_PATH=./go-cdk-wrapper/libcloudadapter.so \
        python tests/benchmark_cgo.py
"""

import ctypes
import json
import os
import statistics
import time

from opencloud.adapter import CloudAdapterBase

SO_PATH = os.environ.get(
    "OPENCLD_SO_PATH", "./go-cdk-wrapper/libcloudadapter.so"
)
ITERATIONS = 500
BUDGET_MS = 2.0


def main() -> int:
    adapter = CloudAdapterBase(so_path=SO_PATH)
    func = adapter.lib.CloudAdapter_GetVersion

    # Warm up so the first-call dynamic-link cost is not measured.
    for _ in range(20):
        ctypes.string_at(func(b"{}"))

    samples = []
    for _ in range(ITERATIONS):
        start = time.perf_counter()
        payload = ctypes.string_at(func(b"{}"))
        samples.append((time.perf_counter() - start) * 1000.0)
        json.loads(payload)

    mean = statistics.fmean(samples)
    p95 = statistics.quantiles(samples, n=20)[18]
    max_ms = max(samples)

    print(f"iterations : {ITERATIONS}")
    print(f"mean       : {mean:.4f} ms")
    print(f"p95        : {p95:.4f} ms")
    print(f"max        : {max_ms:.4f} ms")
    print(f"budget     : {BUDGET_MS:.2f} ms")

    within_budget = mean < BUDGET_MS and p95 < BUDGET_MS
    print(f"verdict    : {'PASS' if within_budget else 'FAIL'}")
    return 0 if within_budget else 1


if __name__ == "__main__":
    raise SystemExit(main())
