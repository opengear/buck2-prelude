#!/usr/bin/env python3
# Copyright (c) Meta Platforms, Inc. and affiliates.
#
# This source code is dual-licensed under either the MIT license found in the
# LICENSE-MIT file in the root directory of this source tree or the Apache
# License, Version 2.0 found in the LICENSE-APACHE file in the root directory
# of this source tree. You may select, at your option, one of the
# above-listed licenses.

# A linker wrapper that links and splits debug info in one action. It takes the
# real linker command line, which holds exactly one `-o <output>`, the path the
# stripped binary goes to. rustc runs it as `-Clinker`. The wrapper links to a
# `$BUCK_SCRATCH_PATH` scratch path (the full-debug binary, never a Buck output,
# so CAS does not store its DWARF), then:
#   - objcopy --only-keep-debug  scratch -> the declared `.debuginfo` sidecar
#   - objcopy --strip-debug --keep-file-symbols --add-gnu-debuglink
#                                scratch -> <output>  (the stripped binary)
#
# These two objcopy passes mirror `strip_debug_with_gnu_debuglink` in
# prelude/linking/strip.bzl, which runs them as separate actions over a declared
# artifact and so cannot run inside a link. Keep the two recipes in sync.
# `--keep-file-symbols` keeps a symbol table in the stripped binary, so on-device
# backtraces resolve names without the sidecar.

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
        help="The real linker command line, with one -o <output>",
    )
    return Args(**vars(parser.parse_args()))


def split_output_flag(linker: list[str]) -> tuple[list[str], str]:
    """Remove the `-o <output>` from the linker args, returning the remaining
    args and the output path.

    The scan reads argv only, and it expects exactly one `-o`. A missing or
    duplicate `-o` makes the wrapper exit with an error. Past the OS limit on
    command-line length, rustc writes every linker argument, including `-o`, to
    a single `@file`. The scan then finds no `-o`, and the link fails.
    """
    rest = []
    output = None
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
        if output is not None:
            eprint(
                "strip_link_action: multiple -o in linker command line: "
                "'{}' and '{}'".format(output, out)
            )
            sys.exit(1)
        output = out
    if output is None:
        # rustc appends its arguments after the wrapper's own, and past the OS
        # command-line limit it passes them all as one trailing `@file`.
        response_file = rest[-1] if rest and rest[-1].startswith("@") else None
        if response_file is not None:
            eprint(
                "strip_link_action: -o not found in the linker command line, which "
                "ends in response file '{}'. Past the OS length limit, rustc writes "
                "every argument to a response file, and this wrapper does not "
                "read response files".format(
                    response_file[1:]
                )
            )
        else:
            eprint("strip_link_action: -o not found in the linker command line")
        sys.exit(1)
    return rest, output


async def _run(*cmd: str) -> int:
    proc = await asyncio.create_subprocess_exec(*cmd, env=os.environ, limit=1_000_000)
    return await proc.wait()


async def main() -> int:
    args = arg_parse()

    linker_bin = args.linker[:1]
    rest, output = split_output_flag(args.linker[1:])

    # Scratch is `BUCK_SCRATCH_PATH` when the action sets it, and the system
    # temp dir otherwise. The full-debug binary and the linker argsfile get
    # unique names there, and concurrent links do not collide on one path.
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
        output,
    )
    if res != 0:
        eprint("strip_link_action: stripping debug info to '{}'".format(output))
    return res


sys.exit(asyncio.run(main()))
