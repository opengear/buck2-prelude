# Copyright (c) Meta Platforms, Inc. and affiliates.
#
# This source code is dual-licensed under either the MIT license found in the
# LICENSE-MIT file in the root directory of this source tree or the Apache
# License, Version 2.0 found in the LICENSE-APACHE file in the root directory
# of this source tree. You may select, at your option, one of the
# above-listed licenses.

# Lint results as provider data.
#
# A `LintInfo` built in one cell nests under a `LintInfo` built in another,
# because every cell loads the same transitive-set type from this file. Buck2
# evaluates the prelude once for all cells. It evaluates a `.bzl` file outside
# the prelude once per loading cell, and each evaluation declares its own set
# type.

LintEntry = record(
    # The linter that produced this entry, such as `clippy` or
    # `shellcheck`.
    checker = str,
    # The target whose rule declared this entry.
    target = TargetLabel,
    # The linter's own human-readable output.
    report = Artifact,
    # Buck2 validation JSON:
    # `{"version": 1, "data": {"status": ..., "message": ...}}`.
    verdict = Artifact,
    # The linter's structured diagnostics, one JSON object per line. Set
    # when the verdict is classified from them.
    diagnostics = field(Artifact | None, None),
)

_LintEntryTSet = transitive_set()

LintInfo = provider(
    doc = "A target's lint entries and its dependencies' entries, as " +
          "one transitive set. Read them with `lint_entries`.",
    fields = {
        "tset": provider_field(_LintEntryTSet),
    },
)

def lint_info(actions: AnalysisActions, entries: list[LintEntry] = [], children: list[LintInfo] = []) -> LintInfo:
    """Builds a `LintInfo` with `entries` as this node's value and the
    sets in `children` beneath it."""
    kwargs = {"children": [child.tset for child in children]}
    if entries:
        kwargs["value"] = entries
    return LintInfo(tset = actions.tset(_LintEntryTSet, **kwargs))

def lint_entries(info: LintInfo) -> list[LintEntry]:
    """Returns the entries of every node under `info`. Traversal visits
    each node once, and a node's entries keep the order `lint_info`
    received them in."""
    entries = []
    for node_entries in info.tset.traverse():
        entries += node_entries
    return entries
