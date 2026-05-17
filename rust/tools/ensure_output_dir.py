#!/usr/bin/env python3

# Runs a command, then guarantees a declared directory output exists.
#
# `rustdoc --persist-doctests <dir>` writes nothing and does not create <dir>
# when a crate has no doctests, but the directory is still a declared Buck
# output that downstream actions consume. This wrapper materializes the empty
# directory after a successful command so the declared output is always
# present, without the wrapped command having to know about the Buck contract.
#
# Usage: ensure_output_dir.py <dir> <command> [args...]
# The directory is created only when <command> exits zero; a non-zero exit is
# propagated unchanged and the directory is left as the command produced it.

import os
import subprocess
import sys


def main() -> None:
    if len(sys.argv) < 3:
        raise ValueError("expected <dir> followed by a command to run")
    output_dir = sys.argv[1]
    res = subprocess.run(sys.argv[2:])
    if res.returncode == 0:
        os.makedirs(output_dir, exist_ok=True)
    sys.exit(res.returncode)


if __name__ == "__main__":
    main()
