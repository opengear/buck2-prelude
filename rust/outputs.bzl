# Copyright (c) Meta Platforms, Inc. and affiliates.
#
# This source code is dual-licensed under either the MIT license found in the
# LICENSE-MIT file in the root directory of this source tree or the Apache
# License, Version 2.0 found in the LICENSE-APACHE file in the root directory
# of this source tree. You may select, at your option, one of the
# above-listed licenses.

load(":link_info.bzl", "TransitiveDeps")

# Outputs that are generally associated with object code compilation, as opposed to linking
RustcCompileOutput = record(
    diag_txt = field(Artifact),
    diag_json = field(Artifact),
    # Zero or more Split DWARF debug info files are emitted into this directory
    # with unpredictable filenames.
    dwo_output_directory = field(Artifact | None),
    # The output of the profiler. Set only if `rust_compile` was invoked with a
    # `profile_mode`
    profile_output = field(Artifact | None),
    # LLVM remarks output. Set when profile_mode=ProfileMode("remarks").
    # These point to diag_txt/diag_json since remarks are part of the diagnostic stream.
    remarks_txt = field(Artifact | None),
    remarks_json = field(Artifact | None),
)

# What rustc wrote when it did not link: an rmeta, rlib, object, staticlib, ...
RustcUnlinkedOutput = record(
    output = field(Artifact),
    # `output`, wrapped in a 1-element transitive set.
    singleton_tset = field(TransitiveDeps),
    # `output` with its debug info stripped. Only the archive-shaped artifacts
    # get one: `Emit("rlib")`, and `Emit("link")` of an rlib or staticlib crate.
    stripped_output = field(Artifact | None),
)

# What rustc produced when it also drove the link: a binary, dylib, cdylib or
# proc macro.
RustcLinkedOutput = record(
    output = field(Artifact),
    # `output`, wrapped in a 1-element transitive set.
    singleton_tset = field(TransitiveDeps),
    # `output` with its debug info stripped.
    stripped_output = field(Artifact),
    # Windows .lib artifact for linking against .dll
    import_library = field(Artifact | None),
    pdb = field(Artifact | None),
    dwp_output = field(Artifact | None),
    # Debug info split out at link time: a `.debuginfo` gnu-debuglink sidecar
    # on ELF, a `.dSYM` bundle on Mach-O.
    debuginfo = field(Artifact | None, None),
)

# The link inputs rustc synthesized for a bin crate, captured by the linker
# wrapper rustc was handed in place of a linker. The executable is produced
# from these by `rust_link_binary`.
LinkExtraction = record(
    # cmd_script set via `-Clinker=`.
    linker_wrapper = field(cmd_args),
    # The retained linker args. Contains no paths, only flags, and so is safe
    # to pass along verbatim even under content-based paths.
    out_argsfile = field(Artifact),
    # Extracted link inputs directory.
    out_artifacts_dir = field(Artifact),
    # Basenames of the extracted objects, one per line, in link order.
    out_manifest = field(Artifact),
    # Archive of the rustc-produced objects. Only built when used by
    # distributed thinlto.
    out_archive = field(Artifact | None),
)

# Output of `rust_compile`.
RustcOutput = record(
    compile_output = RustcCompileOutput,
    # What the compile produced beyond its diagnostics: a `RustcLinkedOutput`
    # exactly when rustc performed a link (see `crate_type_linked`), a
    # `LinkExtraction` exactly for bin crates compiled with `Emit("rlib")`, and
    # a `RustcUnlinkedOutput` otherwise.
    product = RustcUnlinkedOutput | RustcLinkedOutput | LinkExtraction,
)

def output_as_diag_subtargets(o: RustcOutput, clippy: RustcOutput) -> dict[str, Artifact]:
    return {
        "check": o.product.output,
        "clippy.json": clippy.compile_output.diag_json,
        "clippy.txt": clippy.compile_output.diag_txt,
        "diag.json": o.compile_output.diag_json,
        "diag.txt": o.compile_output.diag_txt,
    }

# Access to additional outputs from Rust compilation.
#
# This provider is intended to be available from all rules that compile Rust
# code. As a result, it must be different from `RustLinkInfo`, since it should
# not exist on a prebuilt Rust library, but should exist on a binary.
RustcExtraOutputsInfo = provider(
    fields = {
        "clippy": RustcOutput,
        "clippy_incr": RustcOutput,
        "metadata": RustcOutput,
        "metadata_incr": RustcOutput,
        "remarks": RustcOutput | None,
    },
)

# The persisted doctest source tree plus the reusable rustdoc/rustc compile
# context that produced it. rustdoc sets the layout inside `source_dir`, and
# that layout changes with the rustdoc version and the edition. The provider's
# contract covers the directory and the compile context.
RustDoctestSourceInfo = provider(
    # @unsorted-dict-items
    fields = {
        # Directory output of `rustdoc --test --no-run --persist-doctests`.
        "source_dir": Artifact,
        # The library's own `.rlib`. The persist step built the generated
        # doctest bundle against it. A consumer puts its directory on
        # `-Ldependency` so rustc resolves the bundle's dependency on the
        # library by its strict version hash (SVH).
        "crate_rlib": Artifact,
        # The library's own `--extern=<name>=<lib>` in the prelude's extern
        # wire format, which includes dynamic crate names. The persist compile
        # built the generated bundle against this binding. A consumer compiling
        # the generated doctest source passes it to rustc unchanged.
        "crate_extern_args": cmd_args,
        # rustc args reusable for compiling generated doctest sources: target,
        # edition, cfgs, externs, sysroot, linker, remap, and hidden inputs. It
        # leaves out the crate root, `--crate-name`, and `--crate-type`; the
        # consumer sets those for the generated crate.
        "compile_context_args": cmd_args,
        # The doctest env encoded as `rustc_action.py` `--env=`/`--path-env=`
        # wrapper flags, including `RUSTC_BOOTSTRAP=1`. A consumer passes it
        # unchanged to `rustc_action`.
        "env_args": cmd_args,
        # The clippy-driver wrapper that the library's own clippy compile runs.
        # The wrapper itself puts clippy-driver in clippy mode.
        "clippy_wrapper": cmd_args,
        # The toolchain's lint flags as the library's own clippy compile
        # passes them, with `deny_on_check_lints` as `-D`. A consumer passes
        # them before the compile context. A target's `rustc_flags` then
        # override them, as they do in that compile.
        "clippy_lint_flags": cmd_args,
        # The environment the library's own clippy compile gives clippy-driver.
        # It holds a `CLIPPY_CONF_DIR` entry when the library's
        # `clippy_configuration` or the toolchain sets a `clippy.toml`, and is
        # empty otherwise.
        "clippy_env": dict[str, Artifact],
    },
)
