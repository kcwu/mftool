#!/usr/bin/env python3
"""Stress test: dump→load→validate→diff for every .map file in testdata/gen."""

import argparse
import glob
import os
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


def run_file(i, src, tmp_dir):
    """Run dump→load→validate→diff for one map file.

    Returns (i, label, error_log_or_None).
    """
    if stop_event.is_set():
        return i, None, None

    rel = os.path.relpath(src, os.path.join(os.path.dirname(__file__), ".."))
    label = rel
    # Use index to avoid collisions for same-named files in different subdirs
    stem = f"file_{i}"

    toml_file = os.path.join(tmp_dir, f"{stem}.toml")
    result_map = os.path.join(tmp_dir, f"{stem}.map")

    log_lines = [f"=== FAILED: {label} ==="]

    # dump writes to stdout; capture it into the toml file
    if stop_event.is_set():
        return i, None, None
    dump_cmd = [MFTOOL, "dump", "--all", src]
    with open(toml_file, "w") as f:
        result = subprocess.run(dump_cmd, stdout=f, stderr=subprocess.PIPE, text=True)
    log_lines.append(f"--- dump: {' '.join(dump_cmd)} ---")
    if result.stderr.strip():
        log_lines.append(result.stderr.rstrip())
    if result.returncode != 0:
        log_lines.append(f"(exit code {result.returncode})")
        return i, label, "\n".join(log_lines)

    for step_name, cmd in [
        ("load",     [MFTOOL, "load", "-f", "-o", result_map, toml_file]),
        ("validate", [MFTOOL, "validate", result_map]),
        ("diff",     [MFTOOL, "diff", "--ignore-comment", "--ignore-timestamp", src, result_map]),
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
    parser = argparse.ArgumentParser()
    parser.add_argument("--skip", type=int, default=0, metavar="K",
                        help="skip the first K files")
    args = parser.parse_args()

    map_files = sorted(glob.glob(MAP_GLOB, recursive=True))
    if not map_files:
        emit(f"No .map files found matching {MAP_GLOB}")
        sys.exit(1)

    files = map_files[args.skip:]
    total = len(files)
    width = len(str(args.skip + total))
    emit(f"Found {len(map_files)} map files; running {total} with {min(os.cpu_count() or 4, total)} workers …\n")

    completed = 0
    failed = 0

    with tempfile.TemporaryDirectory() as tmp_dir:
        with ThreadPoolExecutor(max_workers=os.cpu_count() or 4) as pool:
            futures = [
                pool.submit(run_file, args.skip + i, f, tmp_dir)
                for i, f in enumerate(files)
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
        emit(f"FAILED after {completed}/{total} files.")
        sys.exit(1)
    else:
        emit(f"All {completed} files passed.")


if __name__ == "__main__":
    main()
