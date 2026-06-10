# Copyright (c) Meta Platforms, Inc. and affiliates.
#
# This source code is dual-licensed under either the MIT license found in the
# LICENSE-MIT file in the root directory of this source tree or the Apache
# License, Version 2.0 found in the LICENSE-APACHE file in the root directory
# of this source tree. You may select, at your option, one of the
# above-listed licenses.

# Model the various "split" debug scenarios (e.g. `-gsplit-dwarf`).
SplitDebugMode = enum(
    # Debug info, if present, is inline in the object file, and will be linked
    # into executables and shared libraries (e.g. traditional behavior when
    # using `-g`).
    "none",
    # Debug info. if present is included in the object file, but will *not* be
    # linked into executables and shared libraries.  This style usually requires
    # an additional step, separate from the link, to combine and package debug
    # info (e.g. `dSYM`, `dwp`).
    "single",
    # Debug info, is present, is separated to .dwo file. This style requires
    # additional step, separate from the link, to combine and package debug
    # info (e.g. `dSYM`, `dwp`).
    "split",
)

# How a shipped binary's debug information is delivered out of band. Derived
# from the toolchain (linker dialect plus `split_debug_mode`) by
# `get_split_debug_container`, never set directly. `separate_debug_info` (a per-target
# attr) is the intent ("separate my debug"); this names the mechanism that intent
# resolves to for the target's binary format.
SplitDebugContainer = enum(
    # No out-of-band separation at the link/strip stage: either the intent is off
    # or a split mode already externalizes the debug into `.dwo`/`.dwp`.
    "none",
    # ELF: strip the binary, moving its DWARF to a `.debuginfo` gnu-debuglink
    # sidecar.
    "gnu_debuglink",
    # Mach-O: a UUID-matched `.dSYM` bundle from `-Csplit-debuginfo=packed`.
    "dsym",
)
