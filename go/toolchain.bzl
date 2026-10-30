# Copyright (c) Meta Platforms, Inc. and affiliates.
#
# This source code is dual-licensed under either the MIT license found in the
# LICENSE-MIT file in the root directory of this source tree or the Apache
# License, Version 2.0 found in the LICENSE-APACHE file in the root directory
# of this source tree. You may select, at your option, one of the
# above-listed licenses.

GoVersion = record(minor = field(int), patch = field(int))

GoDistrInfo = provider(
    # @unsorted-dict-items
    fields = {
        "bin_go": provider_field(RunInfo),
        "go_root": provider_field(Artifact | None),
        "tool_asm": provider_field(RunInfo),
        "tool_compile": provider_field(RunInfo),
        "tool_cover": provider_field(RunInfo),
        "tool_cgo": provider_field(RunInfo),
        "tool_link": provider_field(RunInfo),
    },
)

GoToolchainInfo = provider(
    # @unsorted-dict-items
    fields = {
        "allow_cache_upload": provider_field(bool | None, default = None),
        "assembler": provider_field(RunInfo),
        "assembler_flags": provider_field(typing.Any, default = []),
        # cmd/buildid. The link runs it to read the main package's build ID
        # and to write the binary's content ID. When unset, a binary's
        # build ID is the link's action ID twice.
        "buildid": provider_field(RunInfo | None, default = None),
        "cxx_compiler_flags": provider_field(typing.Any, default = []),
        "cgo": provider_field(RunInfo),
        "gen_embedcfg": provider_field(RunInfo),
        "go_wrapper": provider_field(RunInfo),
        # The token `go_wrapper` replaces with its working directory in its
        # arguments and in the values of its `--hash-env` variables.
        # `go_wrapper` computes the action ID before it replaces the token.
        # A path written with the token then hashes the same in every action
        # root.
        "go_wrapper_cwd_placeholder": provider_field(str),
        "compiler": provider_field(RunInfo),
        "compiler_flags": provider_field(typing.Any, default = []),
        "pkg_analyzer": provider_field(RunInfo),
        "cover": provider_field(RunInfo),
        "env_go_arch": provider_field(str),
        "env_go_os": provider_field(str),
        # The value cmd/go's cfg.GetArchEnv returns: the setting of the
        # architecture-specific environment variable, such as GOAMD64 or
        # GOARM64. arch_env_key names that variable for a GOARCH. When
        # unset, the rules leave the variable out of the environment, and
        # the Go tools use their own default.
        "env_go_arch_env": provider_field(str | None, default = None),
        "env_go_root": provider_field(Artifact | None, default = None),
        "env_go_debug": provider_field(dict[str, str], default = {}),
        "env_go_experiment": provider_field(list[str], default = []),
        "external_linker_flags": provider_field(typing.Any, default = []),
        "go": provider_field(RunInfo),
        "linker": provider_field(RunInfo),
        "linker_flags": provider_field(typing.Any, default = []),
        "packer": provider_field(RunInfo),
        "build_tags": provider_field(list[str], default = []),
        "asan": provider_field(bool, default = False),
        "race": provider_field(bool, default = False),
        "fuzz": provider_field(bool, default = False),
        # The output of `go version` for this toolchain's `go`. Parse it
        # with parse_go_version_output inside a dynamic action or a BXL
        # dynamic_output.
        "version": provider_field(Artifact),
    },
)

# The architecture-specific environment variable cmd/go's cfg.GetArchEnv
# names for each GOARCH.
_GO_ARCH_ENV_KEYS = {
    "386": "GO386",
    "amd64": "GOAMD64",
    "arm": "GOARM",
    "arm64": "GOARM64",
    "mips": "GOMIPS",
    "mipsle": "GOMIPS",
    "mips64": "GOMIPS64",
    "mips64le": "GOMIPS64",
    "ppc64": "GOPPC64",
    "ppc64le": "GOPPC64",
    "riscv64": "GORISCV64",
    "wasm": "GOWASM",
}

def arch_env_key(goarch: str) -> str:
    """The architecture-specific environment variable cmd/go's
    cfg.GetArchEnv names for goarch. Fails, naming goarch, for a GOARCH
    outside _GO_ARCH_ENV_KEYS.
    """
    if goarch not in _GO_ARCH_ENV_KEYS:
        fail("Looking up the architecture-specific environment variable: cmd/go defines none for GOARCH '{}'; it defines one for {}".format(goarch, ", ".join(sorted(_GO_ARCH_ENV_KEYS))))
    return _GO_ARCH_ENV_KEYS[goarch]

def get_toolchain_env_vars(toolchain: GoToolchainInfo) -> dict[str, str | cmd_args | Artifact]:
    env = {
        "GOARCH": toolchain.env_go_arch,
        "GOEXPERIMENT": ",".join(toolchain.env_go_experiment),
        "GOOS": toolchain.env_go_os,
    }

    if toolchain.env_go_arch_env != None:
        env[arch_env_key(toolchain.env_go_arch)] = toolchain.env_go_arch_env
    if toolchain.env_go_debug:
        godebug = ",".join(["{}={}".format(k, v) for k, v in toolchain.env_go_debug.items()])
        env["GODEBUG"] = godebug

    return env

def evaluate_cgo_enabled(cgo_enabled: bool, override_cgo_enabled: bool | None = None) -> bool:
    if override_cgo_enabled != None:
        return override_cgo_enabled

    return cgo_enabled

# Parse Go version from string, accepted format:
# - "1.20"
# - "1.22rc1" (for pre-release versions)
# - "1.22.1" (for patch versions)
# - "devel" (for development purposes)
def parse_go_version(version: str) -> GoVersion | None:
    if version == "devel":
        return None

    version_parts = version.split(".")

    if len(version_parts) < 2 or len(version_parts) > 3:
        fail("Invalid Go version: '{}'. Expected format '1.20', '1.22rc1', '1.22.1' or 'devel'.".format(version))

    if version_parts[0] != "1":
        fail("Invalid Go version: '{}'. Expected major version to be '1'.".format(version))

    if version_parts[1].isdigit():
        minor = int(version_parts[1])
    elif "rc" in version_parts[1]:
        rc_parts = version_parts[1].split("rc")
        if not (rc_parts[0].isdigit() and rc_parts[1].isdigit()):
            fail("Invalid Go version: '{}'. Expected minor version to be a number with an optional RC suffix like 'rc1'.".format(version))
        minor = int(rc_parts[0])
    else:
        fail("Invalid Go version: '{}'. Expected minor version to be a number with an optional RC suffix like 'rc1'.".format(version))

    patch = 0
    if len(version_parts) > 2:
        if not version_parts[2].isdigit():
            fail("Invalid Go version: '{}'. Expected patch version to be a number.".format(version))

        patch = int(version_parts[2])

    return GoVersion(minor = minor, patch = patch)

# Declares an action that runs `go version` and returns the artifact
# holding its output. The release is readable only from that artifact's
# content, because analysis cannot run a process.
def go_version_file(actions: AnalysisActions, go_wrapper: RunInfo, go: RunInfo, go_root: Artifact, allow_cache_upload: bool | None) -> Artifact:
    output = actions.declare_output("go_version.txt", has_content_based_path = True)
    actions.run(
        cmd_args(go_wrapper, ["--go", go], ["--output", output.as_output()], "--", "version"),
        env = {
            # `go` is a projection of `bin/go`, and Buck2 gives an action only
            # the projected path of a projected artifact. The sandbox then
            # lacks GOROOT's `pkg/tool`, and `go` cannot locate its root from
            # its own path (findGOROOT in cmd/go/internal/cfg). Setting
            # GOROOT points `go` at the tree.
            "GOROOT": go_root,
            # GOTOOLCHAIN=local keeps `go` on this tree when a go.mod in the
            # working directory names a newer release. GOENV=off keeps the user's
            # `go env -w` file, which can set GOTOOLCHAIN and GOFLAGS, out of the
            # run.
            "GOENV": "off",
            "GOTOOLCHAIN": "local",
        },
        category = "go_version",
        allow_cache_upload = allow_cache_upload,
    )
    return output

# `go version` prints `go version go<release> <goos>/<goarch>`, or `go
# version devel ...` for a development build. Parses that first line and
# returns the release, or `None` for a development build.
def parse_go_version_output(output: str) -> GoVersion | None:
    first_line = output.splitlines()[0] if output else ""
    parts = first_line.split()
    if len(parts) >= 3 and parts[0] == "go" and parts[1] == "version":
        if parts[2] == "devel":
            return None
        if parts[2].startswith("go"):
            return parse_go_version(parts[2].removeprefix("go"))
    fail("Parsing `go version` output: first line '{}' lacks the form 'go version go<release>' or 'go version devel'".format(first_line))
