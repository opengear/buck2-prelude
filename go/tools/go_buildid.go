/*
 * Copyright (c) Meta Platforms, Inc. and affiliates.
 *
 * This source code is dual-licensed under either the MIT license found in the
 * LICENSE-MIT file in the root directory of this source tree or the Apache
 * License, Version 2.0 found in the LICENSE-APACHE file in the root directory
 * of this source tree. You may select, at your option, one of the
 * above-listed licenses.
 */

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
)

// goBuildEnv lists the environment variables the action ID hashes: the
// ones cmd/go hashes in buildActionID and printLinkerConfig
// (cmd/go/internal/work/exec.go), and GODEBUG. GODEBUG is in the list
// because the rules pass the toolchain's GODEBUG to every tool. The tools
// do not read it. cmd/go hashes the package's default GODEBUG, which these
// rules do not set.
var goBuildEnv = []string{
	"GOOS", "GOARCH",
	"GOARM", "GOARM64", "GOAMD64", "GO386", "GOMIPS", "GOMIPS64", "GOPPC64", "GORISCV64", "GOWASM",
	"GOEXPERIMENT", "GODEBUG", "GO_EXTLINK_ENABLED",
	"GOCLOBBERDEADHASH", "GOSSAFUNC", "GOSSADIR", "GOCOMPILEDEBUG",
}

// actionMetadataVersion is the version of Buck2's action metadata format
// that actionID reads.
const actionMetadataVersion = 1

// actionMetadata is the shape of the action metadata file Buck2 writes for
// metadata_env_var and metadata_path on actions.run. actionID reads its
// version and each entry's digest.
type actionMetadata struct {
	Version int `json:"version"`
	Digests []struct {
		Digest string `json:"digest"`
	} `json:"digests"`
}

// unsetHashEnvError reports a hashEnv variable that the environment leaves
// unset. A variable set to the empty string hashes as that empty value.
type unsetHashEnvError struct {
	Name string
}

func (e *unsetHashEnvError) Error() string {
	return fmt.Sprintf("hashed environment variable %q is unset", e.Name)
}

// actionID hashes these inputs into a Go action ID:
//   - the input digests listed in Buck2's action metadata at metadataPath;
//   - the tool's arguments in toolArgs;
//   - the goBuildEnv variables set in env;
//   - the env values of the variables named in hashEnv, in an order
//     independent of how hashEnv lists them.
//
// It renders the hash in the form cmd/internal/buildid.HashToString
// produces. When env leaves a hashEnv name unset, actionID returns an
// *unsetHashEnvError.
func actionID(metadataPath string, toolArgs []string, env map[string]string, hashEnv []string) (string, error) {
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return "", fmt.Errorf("reading action metadata %q: %w", metadataPath, err)
	}
	var metadata actionMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return "", fmt.Errorf("parsing action metadata %q: %w", metadataPath, err)
	}
	if metadata.Version != actionMetadataVersion {
		return "", fmt.Errorf("action metadata %q has version %d, and go_wrapper reads version %d", metadataPath, metadata.Version, actionMetadataVersion)
	}
	digests := make([]string, 0, len(metadata.Digests))
	for _, entry := range metadata.Digests {
		digests = append(digests, entry.Digest)
	}
	slices.Sort(digests)

	h := sha256.New()
	for _, digest := range digests {
		fmt.Fprintf(h, "input %q\n", digest)
	}
	for _, arg := range toolArgs {
		fmt.Fprintf(h, "arg %q\n", arg)
	}
	for _, key := range goBuildEnv {
		if value, ok := env[key]; ok {
			fmt.Fprintf(h, "env %s=%q\n", key, value)
		}
	}
	for _, key := range slices.Sorted(slices.Values(hashEnv)) {
		value, ok := env[key]
		if !ok {
			return "", &unsetHashEnvError{Name: key}
		}
		fmt.Fprintf(h, "hashed-env %s=%q\n", key, value)
	}
	sum := h.Sum(nil)
	// The 20-character form of cmd/internal/buildid.HashToString.
	return base64.RawURLEncoding.EncodeToString(sum[:15]), nil
}

// runBuildIDTool runs the buildid tool at buildIDTool with args, inheriting
// the wrapper's environment. cmd/buildid reads no environment variable.
func runBuildIDTool(buildIDTool string, stdout io.Writer, args ...string) error {
	cmd := exec.Command(buildIDTool, args...)
	cmd.Stdout = stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running %q: %w", buildIDTool, err)
	}
	return nil
}

// readBuildID returns the build ID that `go tool buildid` reads from file.
func readBuildID(buildIDTool, file string) (string, error) {
	var stdout bytes.Buffer
	if err := runBuildIDTool(buildIDTool, &stdout, file); err != nil {
		return "", fmt.Errorf("reading build ID of %q: %w", file, err)
	}
	id := strings.TrimSpace(stdout.String())
	if id == "" {
		return "", fmt.Errorf("file %q records an empty build ID", file)
	}
	return id, nil
}

// writeContentID sets the content ID, the last component of file's build
// ID, to the hash of the file's contents, the way `go build` does after it
// links.
func writeContentID(buildIDTool, file string) error {
	if err := runBuildIDTool(buildIDTool, os.Stderr, "-w", file); err != nil {
		return fmt.Errorf("writing content ID of %q: %w", file, err)
	}
	return nil
}

// cwdPlaceholder marks the working directory in tool arguments and in the
// values of hashEnv variables. The wrapper hashes them with the placeholder
// in place and expands it afterward. Workers with different working
// directories then compute the same action ID.
const cwdPlaceholder = "%cwd%"

// buildIDPlacement selects where toolArgs puts the -buildid flag.
type buildIDPlacement int

const (
	// With buildIDLeadingArg, -buildid=<id> leads the tool's arguments.
	// Flag parsing in compile and link stops at the first source or archive.
	buildIDLeadingArg buildIDPlacement = iota
	// With buildIDInLdflags, toolArgs puts -buildid=<id> at the front of
	// the -ldflags value, and go build passes that value to its link.
	buildIDInLdflags
)

var (
	errNoLdflags       = errors.New("go build arguments lack an -ldflags value")
	errLdflagsBuildID  = errors.New("-ldflags value already sets -buildid")
	errMultipleLdflags = errors.New("go build arguments have more than one -ldflags")
	// errLdflagsPattern reports an -ldflags value in the per-package
	// pattern=flags form. A -buildid in front of it makes cmd/go pass the
	// pattern to the linker as a flag.
	errLdflagsPattern = errors.New("-ldflags value has the per-package pattern=flags form")
)

// buildIDFlags holds the parsed wrapper flags that control the -buildid and
// the content ID.
type buildIDFlags struct {
	Placement      buildIDPlacement
	HashEnv        []string
	ActionIDEnv    string
	MainExport     string
	WriteContentID bool
	BuildIDTool    string
}

// missingFlagError reports wrapper flags that need a flag the command line
// leaves out.
type missingFlagError struct {
	// Flags lists the flags that require the missing one, such as
	// "--main-export".
	Flags []string
	// Needs is the missing flag, such as "--action-id-env".
	Needs string
}

func (e *missingFlagError) Error() string {
	quoted := make([]string, len(e.Flags))
	for i, name := range e.Flags {
		quoted[i] = fmt.Sprintf("%q", name)
	}
	verb := "needs"
	if len(e.Flags) > 1 {
		verb = "need"
	}
	return fmt.Sprintf("%s %s %q", strings.Join(quoted, " and "), verb, e.Needs)
}

// checkBuildIDFlags returns a *missingFlagError when a flag is set without
// a flag it requires. --buildid-ldflags, --hash-env, --main-export and
// --write-content-id each require --action-id-env. The last two also
// require --buildid-tool.
func checkBuildIDFlags(f buildIDFlags) error {
	if f.Placement == buildIDInLdflags && f.ActionIDEnv == "" {
		return &missingFlagError{Flags: []string{"--buildid-ldflags"}, Needs: "--action-id-env"}
	}
	if len(f.HashEnv) > 0 && f.ActionIDEnv == "" {
		return &missingFlagError{Flags: []string{"--hash-env"}, Needs: "--action-id-env"}
	}
	if f.MainExport != "" || f.WriteContentID {
		contentIDFlags := []string{"--main-export", "--write-content-id"}
		if f.BuildIDTool == "" {
			return &missingFlagError{Flags: contentIDFlags, Needs: "--buildid-tool"}
		}
		if f.ActionIDEnv == "" {
			return &missingFlagError{Flags: contentIDFlags, Needs: "--action-id-env"}
		}
	}
	return nil
}

// buildIDInputs holds the values a -buildid is composed from. With an
// empty MetadataPath, toolArgs leaves the -buildid out.
type buildIDInputs struct {
	MetadataPath string
	Env          map[string]string
	HashEnv      []string
	MainBuildID  string
	Placement    buildIDPlacement
}

// toolArgs returns the wrapped tool's arguments with every %cwd% replaced by
// cwd. When inputs.MetadataPath is set, toolArgs also adds a -buildid at
// inputs.Placement. actionID computes its action ID from args and inputs
// before the %cwd% replacement (see cwdPlaceholder). With a non-empty
// inputs.MainBuildID, the -buildid value is
// <actionID>/<MainBuildID>/<actionID>.
// buildIDInLdflags needs exactly one -ldflags argument with a value. That
// value must lack a -buildid and must not take the per-package
// pattern=flags form.
func toolArgs(args []string, inputs buildIDInputs, cwd string) ([]string, error) {
	if inputs.MetadataPath != "" {
		var place func(buildIDArg string) []string
		switch inputs.Placement {
		case buildIDInLdflags:
			var err error
			place, err = ldflagsPlacer(args)
			if err != nil {
				return nil, fmt.Errorf("placing build ID in -ldflags: %w", err)
			}
		case buildIDLeadingArg:
			place = func(buildIDArg string) []string {
				return append([]string{buildIDArg}, args...)
			}
		default:
			return nil, fmt.Errorf("placing build ID: unknown placement %d", inputs.Placement)
		}
		id, err := actionID(inputs.MetadataPath, args, inputs.Env, inputs.HashEnv)
		if err != nil {
			return nil, fmt.Errorf("computing action ID: %w", err)
		}
		buildID := id + "/" + id
		if inputs.MainBuildID != "" {
			buildID = id + "/" + inputs.MainBuildID + "/" + id
		}
		args = place("-buildid=" + buildID)
	}
	expanded := make([]string, len(args))
	for i, arg := range args {
		expanded[i] = strings.ReplaceAll(arg, cwdPlaceholder, cwd)
	}
	return expanded, nil
}

// ldflagsPlacer finds the one -ldflags argument in args and returns a
// function that copies args with a -buildid flag at the front of the
// -ldflags value. Like cmd/go, ldflagsPlacer trims white space around the
// value before reading it. The check for an existing -buildid splits the
// value on white space and ignores quoting: it also rejects a -buildid
// token inside a quoted -X value.
func ldflagsPlacer(args []string) (func(buildIDArg string) []string, error) {
	found := scanFlag(args, "ldflags")
	switch {
	case len(found) == 0:
		return nil, fmt.Errorf("arguments %q: %w", args, errNoLdflags)
	case len(found) > 1:
		return nil, fmt.Errorf("arguments %q and %q: %w", args[found[0].Index], args[found[1].Index], errMultipleLdflags)
	}
	ldflags := found[0]
	if ldflags.Form == flagValueAbsent {
		return nil, fmt.Errorf("%q ends arguments %q without a value: %w", args[ldflags.Index], args, errNoLdflags)
	}
	value := strings.TrimSpace(ldflags.Value)
	if !strings.HasPrefix(value, "-") && strings.Contains(value, "=") {
		return nil, fmt.Errorf("-ldflags value %q: %w", value, errLdflagsPattern)
	}
	for _, field := range strings.Fields(value) {
		if name, _, _ := strings.Cut(strings.TrimLeft(field, "-"), "="); name == "buildid" {
			return nil, fmt.Errorf("-ldflags field %q: %w", field, errLdflagsBuildID)
		}
	}
	return func(buildIDArg string) []string {
		placed := slices.Clone(args)
		flags := buildIDArg
		if value != "" {
			flags += " " + value
		}
		if ldflags.Form == flagValueAttached {
			placed[ldflags.Index] = strings.TrimSuffix(args[ldflags.Index], ldflags.Value) + flags
		} else {
			placed[ldflags.Index+1] = flags
		}
		return placed
	}, nil
}

// childEnv returns env as the KEY=value list for the wrapped tool. It
// replaces %cwd% with cwd only in the values of the hashEnv variables.
func childEnv(env map[string]string, hashEnv []string, cwd string) []string {
	list := make([]string, 0, len(env))
	for k, v := range env {
		if slices.Contains(hashEnv, k) {
			v = strings.ReplaceAll(v, cwdPlaceholder, cwd)
		}
		list = append(list, k+"="+v)
	}
	return list
}
