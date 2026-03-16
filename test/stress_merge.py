#!/usr/bin/env python3
"""Stress test: merge two random map files, then validate the result.

Default mode: exhaustive (i, j) pairs of all .map files.
With --random -n N: repeatedly pick 2 files at random and merge them.
"""

import argparse
import glob
import os
import random
import subprocess
import sys
import tempfile
import threading
from concurrent.futures import ThreadPoolExecutor, wait, FIRST_COMPLETED

MFTOOL = os.path.join(os.path.dirname(__file__), "..", "mftool")
MAP_GLOB = os.path.join(os.path.dirname(__file__), "..", "testdata", "gen", "**", "*.map")

stop_event = threading.Event()
print_lock = threading.Lock()


def emit(msg):
    """Write a line to stderr — unbuffered and not captured by VSCode's stdout pipe."""
    os.write(2, (msg + "\n").encode())


def run(args):
    result = subprocess.run(
        args,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
    )
    return result.returncode, result.stdout


def run_merge(i, files, tmp_dir):
    """Merge two map files and validate the result.

    Returns (i, label, error_log_or_None).
    """
    if stop_event.is_set():
        return i, None, None

    label = " + ".join(os.path.basename(f) for f in files)
    result_map = os.path.join(tmp_dir, f"{i}_merged.map")

    log_lines = [f"=== FAILED: {label} ==="]

    for step_name, cmd in [
        ("merge",    [MFTOOL, "merge", "-f", "-o", result_map] + files),
        ("validate", [MFTOOL, "validate", result_map]),
    ]:
        if stop_event.is_set():
            return i, None, None
        rc, output = run(cmd)
        log_lines.append(f"--- {step_name}: {' '.join(cmd)} ---")
        if output.strip():
            log_lines.append(output.rstrip())
        if rc != 0:
            log_lines.append(f"(exit code {rc})")
            return i, label, "\n".join(log_lines)

    return i, label, None


def main():
    parser = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--skip", type=int, default=0, metavar="K",
                        help="skip the first K jobs")
    parser.add_argument("--random", action="store_true",
                        help="random mode: repeatedly pick 2 files at random")
    parser.add_argument("--rounds", type=int, default=100, metavar="R",
                        help="number of random merges to run (default: 100)")
    parser.add_argument("--seed", type=int, default=None, metavar="S",
                        help="random seed for reproducibility")
    args = parser.parse_args()

    map_files = sorted(glob.glob(MAP_GLOB, recursive=True))
    if not map_files:
        emit(f"No .map files found matching {MAP_GLOB}")
        sys.exit(1)

    if args.random:
        if len(map_files) < 2:
            emit(f"Error: need at least 2 map files, found {len(map_files)}")
            sys.exit(1)
        rng = random.Random(args.seed)
        if args.skip:
            emit(f"Skipping first {args.skip} merges.")
            for _ in range(args.skip):
                rng.sample(map_files, 2)
        jobs = (rng.sample(map_files, 2) for _ in range(args.rounds - args.skip))
        total = args.rounds - args.skip
        emit(f"Random mode: {args.rounds} merges from {len(map_files)} map files "
             f"(seed={args.seed})\n")
    else:
        pairs = [(a, b) for a in map_files for b in map_files]
        if args.skip:
            emit(f"Skipping first {args.skip} pairs.")
            pairs = pairs[args.skip:]
        jobs = [[a, b] for a, b in pairs]
        total = len(jobs)
        names = [os.path.basename(f) for f in map_files]
        emit(f"Found {len(map_files)} map files: {', '.join(names)}")
        emit(f"Running {total} pairs with {min(os.cpu_count() or 4, total)} workers …\n")

    width = len(str(args.skip + total))
    completed = 0
    failed = 0

    max_workers = os.cpu_count() or 4
    with tempfile.TemporaryDirectory() as tmp_dir:
        with ThreadPoolExecutor(max_workers=max_workers) as pool:
            job_iter = iter(enumerate(jobs))
            pending = set()

            def fill_queue():
                while len(pending) < max_workers * 2:
                    try:
                        i, pair = next(job_iter)
                        pending.add(pool.submit(run_merge, i, pair, tmp_dir))
                    except StopIteration:
                        break

            fill_queue()
            try:
                while pending:
                    done, remaining = wait(pending, return_when=FIRST_COMPLETED)
                    pending = set(remaining)
                    for fut in done:
                        _, label, err = fut.result()
                        if label is None:
                            continue  # skipped due to stop_event
                        with print_lock:
                            completed += 1
                            n = args.skip + completed
                            grand_total = args.skip + total
                            if err:
                                failed += 1
                                emit(f"[{n:>{width}}/{grand_total}] FAIL  {label}")
                                stop_event.set()
                                emit(err)
                                pending.clear()
                                break
                            else:
                                emit(f"[{n:>{width}}/{grand_total}] ok    {label}")
                        if not stop_event.is_set():
                            fill_queue()
            except KeyboardInterrupt:
                stop_event.set()
                emit("\nInterrupted.")
                sys.exit(1)

    emit("")
    if failed:
        emit(f"FAILED after {completed}/{total} {'merges' if args.random else 'pairs'}.")
        sys.exit(1)
    else:
        emit(f"All {completed} {'merges' if args.random else 'pairs'} passed.")


if __name__ == "__main__":
    main()
