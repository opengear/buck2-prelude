# Copyright (c) Meta Platforms, Inc. and affiliates.
#
# This source code is dual-licensed under either the MIT license found in the
# LICENSE-MIT file in the root directory of this source tree or the Apache
# License, Version 2.0 found in the LICENSE-APACHE file in the root directory
# of this source tree. You may select, at your option, one of the
# above-listed licenses.

load("@prelude//:paths.bzl", "paths")
load(
    ":toolchain.bzl",
    "GoToolchainInfo",
)
load(
    ":packages.bzl",
    "GoPkg",  # @Unused used as type
    "merge_pkgs",
)

# Provider wrapping packages used for compiling.
GoPkgCompileInfo = provider(
    fields = {
        "pkgs": provider_field(typing.Any, default = None),  # dict[str, GoPkg]
    }
)

# Provider for test targets that test a library. Contains information for
# compiling the test and library code together as expected by go.
GoTestInfo = provider(
    # @unsorted-dict-items
    fields = {
        "deps": provider_field(typing.Any, default = None),  # [Dependency]
        "srcs": provider_field(typing.Any, default = None),  # ["source"]
        "pkg_import_path": provider_field(typing.Any, default = None),  # str
        "coverage_enabled": provider_field(bool),
    },
)

def get_inherited_compile_pkgs(deps: list[Dependency]) -> dict[str, GoPkg]:
    return merge_pkgs([d[GoPkgCompileInfo].pkgs for d in deps if GoPkgCompileInfo in d])

def infer_package_root(srcs: list[Artifact]) -> str:
    go_sources = [s for s in srcs if s.extension == ".go"]
    if len(go_sources) == 0:
        return ""
    dir_set = set([paths.dirname(s.short_path) for s in go_sources])
    if len(dir_set) > 1:
        fail(
            "Provide `package_root` target attribute. Can't infer it when there are multiple directories containing .go files: {}. Sources: {}".format(
                dir_set,
                [s.short_path for s in go_sources],
            )
        )

    return list(dir_set)[0]

def trimpath_spec(go_toolchain: GoToolchainInfo, srcs: list[Artifact], pkg_import_path: str, gen_dir: Artifact | None = None) -> cmd_args:
    """The `-trimpath` rewrites for one package, in the grammar of
    `cmd/internal/objabi` `ApplyRewrites`, where the first matching rule
    wins. `gen_dir` is the directory of cgo's generated files, and its
    rules come first. Under them a generated file keeps only its base
    name, and a `//line` path that the compiler joined onto `gen_dir`
    loses that prefix. A tool records every other source as
    `<pkg_import_path>/<file>`, the form `go build -trimpath` writes,
    whichever directory the source is in: a package can list checked-in
    and generated files together. `go_wrapper` replaces the toolchain's
    `go_wrapper_cwd_placeholder` token with the working directory's
    absolute path, and the last rule trims that prefix from any remaining
    file. Fails, naming pkg_import_path, when srcs is empty."""
    if len(srcs) == 0:
        fail("Composing -trimpath for package '{}': the tool run lists no sources".format(pkg_import_path))

    cwd = go_toolchain.go_wrapper_cwd_placeholder
    rules = []
    if gen_dir:
        rules.append(cmd_args(cwd, "/", gen_dir, "=>", delimiter = ""))
        rules.append(cmd_args(gen_dir, "=>", delimiter = ""))

    gen_key = (False, str(gen_dir.owner), gen_dir.short_path) if gen_dir else None
    first_src_by_dir = {}
    dirs_in_order = []
    for src in srcs:
        key = (src.is_source, str(src.owner) if src.owner else "", paths.dirname(src.short_path))
        if key == gen_key or key in first_src_by_dir:
            continue
        first_src_by_dir[key] = src
        dirs_in_order.append(key)

    rules.extend([cmd_args(cwd, "/", cmd_args(first_src_by_dir[key], parent = 1), "=>", pkg_import_path, delimiter = "") for key in dirs_in_order])
    rules.append(cwd)
    return cmd_args(rules, delimiter = ";")
