#!/usr/bin/env python3
# Copyright (c) Meta Platforms, Inc. and affiliates.
#
# This source code is dual-licensed under either the MIT license found in the
# LICENSE-MIT file in the root directory of this source tree or the Apache
# License, Version 2.0 found in the LICENSE-APACHE file in the root directory
# of this source tree. You may select, at your option, one of the
# above-listed licenses.

# A `-Clinker={}` wrapper that links and splits debug info in one action. rustc
# invokes this with the real linker command line, including `-o <rustc_temp>`
# (the temporary rustc later copies to its `--emit=link` output). The wrapper
# redirects the link to a `$BUCK_SCRATCH_PATH` scratch path (the full-debug
# binary, never a Buck output, so its DWARF is not stored in CAS), then:
#   - objcopy --only-keep-debug  scratch -> the declared `.debuginfo` sidecar
#   - objcopy --strip-debug --keep-file-symbols --add-gnu-debuglink
#                                scratch -> <rustc_temp>  (the stripped binary)
# rustc then copies the stripped <rustc_temp> to its declared link output.
#
# These two objcopy passes mirror `strip_debug_with_gnu_debuglink` in
# prelude/linking/strip.bzl, which splits a declared cxx binary the same way.
# That helper runs as separate analysis-time actions over a declared Artifact,
# so it cannot run here inside rustc's link as the linker subprocess; the recipe
# is duplicated and the two must stay in sync. `--keep-file-symbols` is
# deliberate (not the `--strip-unneeded` strip used elsewhere): the stripped
# binary keeps a symbol table so on-device backtraces resolve names without the
# sidecar.

import argparse
import asyncio
import os
import sys
import tempfile
from typing import Any, NamedTuple


def eprint(*args: Any, **kwargs: Any) -> None:
    print(*args, end="\n", file=sys.stderr, flush=True, **kwargs)


class Args(NamedTuple):
    objcopy: str
    debuginfo_out: str
    linker: list[str]


def arg_parse() -> Args:
    parser = argparse.ArgumentParser()
    parser.add_argument("--objcopy", required=True, help="objcopy binary")
    parser.add_argument(
        "--debuginfo_out", required=True, help="Declared output: DWARF sidecar"
    )
    parser.add_argument(
        "linker",
        nargs=argparse.REMAINDER,
        type=str,
        help="The real linker command line (includes rustc's -o <temp>)",
    )
    return Args(**vars(parser.parse_args()))


def split_output_flag(linker: list[str]) -> tuple[list[str], str]:
    """Remove rustc's `-o <temp>` from the linker args, returning the remaining
    args and the temp path rustc expects the linked binary at.

    rustc always passes `-o` on the linker command line, never inside an
    `@argsfile`, so scanning argv is sufficient. Exactly one `-o` is expected; a
    missing or duplicate `-o` fails loudly rather than guessing.
    """
    rest = []
    rustc_temp = None
    i = 0
    n = len(linker)
    while i < n:
        arg = linker[i]
        out = None
        if arg == "-o" and i + 1 < n:
            out = linker[i + 1]
            i += 2
        elif arg.startswith("-o") and len(arg) > 2:
            out = arg[2:]
            i += 1
        else:
            rest.append(arg)
            i += 1
            continue
        if rustc_temp is not None:
            eprint(
                "strip_link_action: multiple -o in linker command line: "
                "'{}' and '{}'".format(rustc_temp, out)
            )
            sys.exit(1)
        rustc_temp = out
    if rustc_temp is None:
        eprint("strip_link_action: no -o in linker command line")
        sys.exit(1)
    return rest, rustc_temp


async def _run(*cmd: str) -> int:
    proc = await asyncio.create_subprocess_exec(*cmd, env=os.environ, limit=1_000_000)
    return await proc.wait()


async def main() -> int:
    args = arg_parse()

    linker_bin = args.linker[:1]
    rest, rustc_temp = split_output_flag(args.linker[1:])

    # Scratch lives in BUCK_SCRATCH_PATH when the action provides it, else the
    # system temp dir (tempfile's default), matching `deferred_link_action.py`.
    # The full-debug binary and the linker argsfile are written here under unique
    # names: neither is a declared output, so Buck never captures the full-debug
    # binary into CAS, and concurrent links never collide on a shared path.
    scratch_dir = os.environ.get("BUCK_SCRATCH_PATH")
    link_fd, link_out = tempfile.mkstemp(prefix="link-full-", dir=scratch_dir)
    os.close(link_fd)

    # Link to scratch (the full-debug binary), passing the bulk of the link line
    # via an argsfile to stay under the command-line limit. `delete=False` so the
    # file outlives this block to reach the linker subprocess.
    with tempfile.NamedTemporaryFile(
        mode="wb",
        prefix="real-linker-args-",
        suffix=".txt",
        delete=False,
        dir=scratch_dir,
    ) as args_file:
        args_file.write("\n".join(rest).encode() + b"\n")
        args_file.flush()
        res = await _run(*linker_bin, "@" + args_file.name, "-o", link_out)
    if res != 0:
        eprint("strip_link_action: linking to '{}'".format(link_out))
        return res

    res = await _run(args.objcopy, "--only-keep-debug", link_out, args.debuginfo_out)
    if res != 0:
        eprint("strip_link_action: extracting debug info to '{}'".format(args.debuginfo_out))
        return res

    res = await _run(
        args.objcopy,
        "--strip-debug",
        "--keep-file-symbols",
        "--add-gnu-debuglink=" + args.debuginfo_out,
        link_out,
        rustc_temp,
    )
    if res != 0:
        eprint("strip_link_action: stripping debug info to '{}'".format(rustc_temp))
    return res


sys.exit(asyncio.run(main()))
