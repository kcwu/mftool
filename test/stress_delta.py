#!/usr/bin/env python3
"""Stress test: run delta→apply→validate→diff for map file chains.

Default mode: exhaustive (i, j) pairs of all .map files.
With --random -n N: repeatedly pick N random files, build a delta chain,
and apply all N-1 deltas in one apply invocation.
"""

import argparse
import glob
import os
import random
import subprocess
import sys
import tempfile
import threading
from concurrent.futures import ThreadPoolExecutor, as_completed

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


def run_chain(i, files, tmp_dir):
    """Run delta chain → apply → validate → diff for a sequence of N files.

    Computes N-1 deltas (files[k] → files[k+1]) then applies them all at once
    to files[0], producing a result that should equal files[-1].
    Returns (i, label, error_log_or_None).
    """
    if stop_event.is_set():
        return i, None, None

    label = " → ".join(os.path.basename(f) for f in files)
    delta_files = [os.path.join(tmp_dir, f"{i}_d{k}.mfd") for k in range(len(files) - 1)]
    result_map = os.path.join(tmp_dir, f"{i}_result.map")

    log_lines = [f"=== FAILED: {label} ==="]

    # Build delta chain
    for k in range(len(files) - 1):
        cmd = [MFTOOL, "delta", "-f", "-o", delta_files[k], files[k], files[k + 1]]
        if stop_event.is_set():
            return i, None, None
        rc, output = run(cmd)
        log_lines.append(f"--- delta[{k}]: {' '.join(cmd)} ---")
        if output.strip():
            log_lines.append(output.rstrip())
        if rc != 0:
            log_lines.append(f"(exit code {rc})")
            return i, label, "\n".join(log_lines)

    # Apply all deltas in one shot
    for step_name, cmd in [
        ("apply",    [MFTOOL, "apply", "-f", "-o", result_map, files[0]] + delta_files),
        ("validate", [MFTOOL, "validate", result_map]),
        ("diff",     [MFTOOL, "diff", "--ignore-comment", "--ignore-timestamp",
                      files[-1], result_map]),
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
                        help="random mode: repeatedly pick N files at random")
    parser.add_argument("-n", type=int, default=2, metavar="N",
                        help="chain length for random mode (default: 2)")
    parser.add_argument("--rounds", type=int, default=100, metavar="R",
                        help="number of random chains to run (default: 100)")
    parser.add_argument("--seed", type=int, default=None, metavar="S",
                        help="random seed for reproducibility")
    args = parser.parse_args()

    map_files = sorted(glob.glob(MAP_GLOB, recursive=True))
    if not map_files:
        emit(f"No .map files found matching {MAP_GLOB}")
        sys.exit(1)

    if args.random:
        if args.n < 2:
            emit("Error: -n must be >= 2")
            sys.exit(1)
        if args.n > len(map_files):
            emit(f"Error: -n {args.n} exceeds number of map files ({len(map_files)})")
            sys.exit(1)
        rng = random.Random(args.seed)
        all_jobs = [rng.sample(map_files, args.n) for _ in range(args.rounds)]
        if args.skip:
            emit(f"Skipping first {args.skip} chains.")
            all_jobs = all_jobs[args.skip:]
        jobs = all_jobs
        total = len(jobs)
        emit(f"Random mode: {args.rounds} chains of length {args.n} "
             f"from {len(map_files)} map files "
             f"(seed={args.seed})\n")
    else:
        pairs = [(s, d) for s in map_files for d in map_files]
        if args.skip:
            emit(f"Skipping first {args.skip} pairs.")
            pairs = pairs[args.skip:]
        jobs = [[s, d] for s, d in pairs]
        total = len(jobs)
        names = [os.path.basename(f) for f in map_files]
        emit(f"Found {len(map_files)} map files: {', '.join(names)}")
        emit(f"Running {total} pairs with {min(os.cpu_count() or 4, total)} workers …\n")

    width = len(str(args.skip + total))
    completed = 0
    failed = 0

    with tempfile.TemporaryDirectory() as tmp_dir:
        with ThreadPoolExecutor(max_workers=os.cpu_count() or 4) as pool:
            futures = [
                pool.submit(run_chain, i, chain, tmp_dir)
                for i, chain in enumerate(jobs)
            ]
            try:
                for fut in as_completed(futures):
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
                            break
                        else:
                            emit(f"[{n:>{width}}/{grand_total}] ok    {label}")
            except KeyboardInterrupt:
                stop_event.set()
                emit("\nInterrupted.")
                sys.exit(1)

    emit("")
    if failed:
        emit(f"FAILED after {completed}/{total} {'chains' if args.random else 'pairs'}.")
        sys.exit(1)
    else:
        emit(f"All {completed} {'chains' if args.random else 'pairs'} passed.")


if __name__ == "__main__":
    main()
