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
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func writeTempFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeActionMetadata writes action metadata that lists one input digest.
func writeActionMetadata(t *testing.T) string {
	t.Helper()
	return writeTempFile(t, "metadata.json", []byte(`{"version": 1, "digests": [{"path": "pkg/a.go", "digest": "8be2d730:2048"}]}`))
}

func TestActionID(t *testing.T) {
	const metadata = `{"version": 1, "digests": [
		{"path": "pkg/b.go", "digest": "c05d61fe:913"},
		{"path": "pkg/a.go", "digest": "8be2d730:2048"}]}`
	const reordered = `{"version": 1, "digests": [
		{"path": "other/a.go", "digest": "8be2d730:2048"},
		{"path": "other/b.go", "digest": "c05d61fe:913"}]}`
	args := []string{"-p", "example.com/pkg", "-o", "pkg.a", "pkg/a.go"}
	env := map[string]string{"GOOS": "linux", "GOARCH": "arm64"}
	with := func(key, value string) map[string]string {
		e := map[string]string{key: value}
		for k, v := range env {
			e[k] = v
		}
		return e
	}
	// Each want is the first 15 bytes, in URL-safe base64, of the SHA-256 of
	// the lines `input "<digest>"` in sorted order, `arg "<arg>"` in order, and
	// `env KEY="<value>"` for each set goBuildEnv variable in goBuildEnv order.
	for _, tc := range []struct {
		name     string
		metadata string
		args     []string
		env      map[string]string
		want     string
	}{
		{"base", metadata, args, env, "5_vgdOVYOj0d83GwKMQp"},
		{"digests reordered under other paths", reordered, args, env, "5_vgdOVYOj0d83GwKMQp"},
		{"variables outside goBuildEnv", metadata, args, with("GOROOT", "/worker/goroot"), "5_vgdOVYOj0d83GwKMQp"},
		{"added argument", metadata, append(slices.Clone(args), "-race"), env, "gc_c2CS63PK97a5n4rdY"},
		{"added GOAMD64", metadata, args, with("GOAMD64", "v3"), "Zfx1wdCZpqzffO-Pwglc"},
		{"added GODEBUG", metadata, args, with("GODEBUG", "asynctimerchan=1"), "wCi1bYO3hJtBlNDdLQbz"},
		{
			"digest moved into the arguments",
			`{"version": 1, "digests": [{"path": "pkg/b.go", "digest": "c05d61fe:913"}]}`,
			append([]string{"8be2d730:2048"}, args...),
			env,
			"OIUFOf5oNn1IrTcSmoq4",
		},
	} {
		got, err := actionID(writeTempFile(t, "metadata.json", []byte(tc.metadata)), tc.args, tc.env, nil)
		if err != nil || got != tc.want {
			t.Errorf("%s: actionID = %q, %v; want %q, nil", tc.name, got, err, tc.want)
		}
	}
}

func TestActionIDRejectsUnreadableMetadata(t *testing.T) {
	for _, tc := range []struct {
		name string
		path func(t *testing.T) string
	}{
		{"missing file", func(t *testing.T) string { return filepath.Join(t.TempDir(), "metadata.json") }},
		{"truncated JSON", func(t *testing.T) string {
			return writeTempFile(t, "metadata.json", []byte(`{"version": 1, "digests": [`))
		}},
		{"version 2", func(t *testing.T) string {
			return writeTempFile(t, "metadata.json", []byte(`{"version": 2, "digests": []}`))
		}},
		{"absent version", func(t *testing.T) string { return writeTempFile(t, "metadata.json", []byte(`{"digests": []}`)) }},
	} {
		if id, err := actionID(tc.path(t), nil, nil, nil); err == nil {
			t.Errorf("%s: actionID = %q, nil; want an error", tc.name, id)
		}
	}
}

func TestActionIDHashesHashEnvVariables(t *testing.T) {
	metadata := writeActionMetadata(t)
	args := []string{"-o", "out", "./cmd"}
	hashEnv := []string{"CC", "CGO_CFLAGS"}
	env := map[string]string{"GOOS": "linux", "CC": "%cwd%/cc", "CGO_CFLAGS": "-I%cwd%/include"}
	with := func(key, value string) map[string]string {
		e := maps.Clone(env)
		e[key] = value
		return e
	}
	base, err := actionID(metadata, args, env, hashEnv)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		env      map[string]string
		hashEnv  []string
		wantSame bool
	}{
		{"changed CC value", with("CC", "%cwd%/clang"), hashEnv, false},
		{"CC set to the empty string", with("CC", ""), hashEnv, false},
		{"names in another order", env, []string{"CGO_CFLAGS", "CC"}, true},
		{"added variable outside hashEnv", with("CGO_LDFLAGS", "-L%cwd%/lib"), hashEnv, true},
	} {
		got, err := actionID(metadata, args, tc.env, tc.hashEnv)
		if err != nil || (got == base) != tc.wantSame {
			t.Errorf("%s: actionID = %q, %v; base ID %q, want same %v", tc.name, got, err, base, tc.wantSame)
		}
	}
}

func TestActionIDRejectsAnUnsetHashEnvVariable(t *testing.T) {
	env := map[string]string{"GOOS": "linux", "CC": "%cwd%/cc"}
	id, err := actionID(writeActionMetadata(t), nil, env, []string{"CC", "CGO_CFLAGS"})
	var unset *unsetHashEnvError
	if !errors.As(err, &unset) || unset.Name != "CGO_CFLAGS" {
		t.Errorf("actionID = %q, %v; want an *unsetHashEnvError naming CGO_CFLAGS", id, err)
	}
}

func TestToolArgs(t *testing.T) {
	metadata := writeActionMetadata(t)
	args := []string{"-trimpath=%cwd%/pkg=>example.com/pkg;%cwd%=>", "-o", "pkg.a", "%cwd%/pkg/a.go"}
	env := map[string]string{"GOOS": "linux", "GOARCH": "arm64"}
	id, err := actionID(metadata, args, env, nil)
	if err != nil {
		t.Fatal(err)
	}
	expanded := func(cwd string) []string {
		return []string{"-trimpath=" + cwd + "/pkg=>example.com/pkg;" + cwd + "=>", "-o", "pkg.a", cwd + "/pkg/a.go"}
	}
	withBuildID := func(buildID, cwd string) []string {
		return append([]string{"-buildid=" + buildID}, expanded(cwd)...)
	}
	// The compile rows under /a and /b want one -buildid, the hash of args
	// as written: two workers that differ only in cwd agree on it.
	for _, tc := range []struct {
		name         string
		metadataPath string
		mainBuildID  string
		cwd          string
		want         []string
		wantErr      bool
	}{
		{"no metadata expands %cwd% only", "", "", "/a", expanded("/a"), false},
		{"compile under /a", metadata, "", "/a", withBuildID(id+"/"+id, "/a"), false},
		{"compile under /b", metadata, "", "/b", withBuildID(id+"/"+id, "/b"), false},
		{"link puts the main build ID between the action IDs", metadata, "M/C", "/a", withBuildID(id+"/M/C/"+id, "/a"), false},
		{"missing metadata file", filepath.Join(t.TempDir(), "metadata.json"), "", "/a", nil, true},
	} {
		got, err := toolArgs(args, buildIDInputs{MetadataPath: tc.metadataPath, Env: env, MainBuildID: tc.mainBuildID, Placement: buildIDLeadingArg}, tc.cwd)
		if !slices.Equal(got, tc.want) || (err != nil) != tc.wantErr {
			t.Errorf("%s: toolArgs = %q, %v; want %q, error %v", tc.name, got, err, tc.want, tc.wantErr)
		}
	}
	if want := expanded("%cwd%"); !slices.Equal(args, want) {
		t.Errorf("args after toolArgs = %q, want %q unchanged", args, want)
	}
}

func TestToolArgsRejectsAnUnknownPlacement(t *testing.T) {
	got, err := toolArgs([]string{"./cmd"}, buildIDInputs{MetadataPath: writeActionMetadata(t), Placement: buildIDPlacement(2)}, "/a")
	if err == nil || got != nil {
		t.Errorf("toolArgs = %q, %v; want nil, an error", got, err)
	}
}

func TestToolArgsPutsBuildIDInLdflags(t *testing.T) {
	metadata := writeActionMetadata(t)
	env := map[string]string{"GOOS": "linux", "GOARCH": "arm64"}
	for _, tc := range []struct {
		name string
		args []string
		want func(buildID string) []string
	}{
		{
			"value as the next argument",
			[]string{"-trimpath", "-ldflags", "-s -w", "-o", "%cwd%/out", "./cmd"},
			func(b string) []string {
				return []string{"-trimpath", "-ldflags", "-buildid=" + b + " -s -w", "-o", "/a/out", "./cmd"}
			},
		},
		{
			"value after =",
			[]string{"-trimpath", "-ldflags=-s -w", "-o", "%cwd%/out", "./cmd"},
			func(b string) []string {
				return []string{"-trimpath", "-ldflags=-buildid=" + b + " -s -w", "-o", "/a/out", "./cmd"}
			},
		},
		{
			"empty value as the next argument",
			[]string{"-ldflags", "", "./cmd"},
			func(b string) []string { return []string{"-ldflags", "-buildid=" + b, "./cmd"} },
		},
		{
			"empty value after =",
			[]string{"-ldflags=", "./cmd"},
			func(b string) []string { return []string{"-ldflags=-buildid=" + b, "./cmd"} },
		},
		{
			"value with leading spaces",
			[]string{"-ldflags", " -X main.version=1.2", "./cmd"},
			func(b string) []string {
				return []string{"-ldflags", "-buildid=" + b + " -X main.version=1.2", "./cmd"}
			},
		},
		{
			"value of spaces only",
			[]string{"-ldflags", "  ", "./cmd"},
			func(b string) []string { return []string{"-ldflags", "-buildid=" + b, "./cmd"} },
		},
		{
			"--ldflags with the value as the next argument",
			[]string{"--ldflags", "-s", "./cmd"},
			func(b string) []string { return []string{"--ldflags", "-buildid=" + b + " -s", "./cmd"} },
		},
		{
			"--ldflags with the value after =",
			[]string{"--ldflags=-s", "./cmd"},
			func(b string) []string { return []string{"--ldflags=-buildid=" + b + " -s", "./cmd"} },
		},
	} {
		id, err := actionID(metadata, tc.args, env, nil)
		if err != nil {
			t.Fatal(err)
		}
		want := tc.want(id + "/" + id)
		got, err := toolArgs(tc.args, buildIDInputs{MetadataPath: metadata, Env: env, Placement: buildIDInLdflags}, "/a")
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("%s: toolArgs = %q, %v; want %q, nil", tc.name, got, err, want)
		}
	}
}

func TestToolArgsRejectsLdflagsThatCannotCarryTheBuildID(t *testing.T) {
	metadata := writeActionMetadata(t)
	for _, tc := range []struct {
		name string
		args []string
		want error
	}{
		{"arguments without -ldflags", []string{"-o", "out", "./cmd"}, errNoLdflags},
		{"-buildid=x in the value", []string{"-ldflags", "-s -buildid=x", "-o", "out", "./cmd"}, errLdflagsBuildID},
		{"-buildid x in the value after =", []string{"-ldflags=-buildid x -s", "-o", "out", "./cmd"}, errLdflagsBuildID},
		{"two -ldflags arguments", []string{"-ldflags", "-s", "-ldflags=-w", "./cmd"}, errMultipleLdflags},
		{"pattern=flags value after =", []string{"-ldflags=all=-s -w", "./cmd"}, errLdflagsPattern},
		{"pattern=flags value as the next argument", []string{"-ldflags", "example.com/cmd=-s", "./cmd"}, errLdflagsPattern},
		{"pattern=flags value after leading spaces", []string{"-ldflags= all=-s", "./cmd"}, errLdflagsPattern},
		{"-ldflags as the last argument", []string{"./cmd", "-ldflags"}, errNoLdflags},
		{"-ldflags and --ldflags", []string{"-ldflags=-s", "--ldflags=-w", "./cmd"}, errMultipleLdflags},
	} {
		got, err := toolArgs(tc.args, buildIDInputs{MetadataPath: metadata, Placement: buildIDInLdflags}, "/a")
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: toolArgs = %q, %v; want error %v", tc.name, got, err, tc.want)
		}
	}
}

func TestCheckBuildIDFlags(t *testing.T) {
	const actionIDEnv = "BUCK_ACTION_METADATA"
	const tool = "/toolchain/buildid"
	contentIDFlags := []string{"--main-export", "--write-content-id"}
	for _, tc := range []struct {
		name  string
		flags buildIDFlags
		want  *missingFlagError
	}{
		{
			"ldflags placement without --action-id-env",
			buildIDFlags{Placement: buildIDInLdflags},
			&missingFlagError{Flags: []string{"--buildid-ldflags"}, Needs: "--action-id-env"},
		},
		{"ldflags placement with --action-id-env", buildIDFlags{Placement: buildIDInLdflags, ActionIDEnv: actionIDEnv}, nil},
		{"leading argument without --action-id-env", buildIDFlags{Placement: buildIDLeadingArg}, nil},
		{
			"--hash-env without --action-id-env",
			buildIDFlags{HashEnv: []string{"CC"}},
			&missingFlagError{Flags: []string{"--hash-env"}, Needs: "--action-id-env"},
		},
		{"--hash-env with --action-id-env", buildIDFlags{HashEnv: []string{"CC"}, ActionIDEnv: actionIDEnv}, nil},
		{
			"--main-export without --buildid-tool",
			buildIDFlags{MainExport: "main.x", ActionIDEnv: actionIDEnv},
			&missingFlagError{Flags: contentIDFlags, Needs: "--buildid-tool"},
		},
		{
			"--write-content-id without --buildid-tool",
			buildIDFlags{WriteContentID: true, ActionIDEnv: actionIDEnv},
			&missingFlagError{Flags: contentIDFlags, Needs: "--buildid-tool"},
		},
		{
			"--main-export without --action-id-env",
			buildIDFlags{MainExport: "main.x", BuildIDTool: tool},
			&missingFlagError{Flags: contentIDFlags, Needs: "--action-id-env"},
		},
		{
			"--write-content-id without --action-id-env",
			buildIDFlags{WriteContentID: true, BuildIDTool: tool},
			&missingFlagError{Flags: contentIDFlags, Needs: "--action-id-env"},
		},
		{
			"every flag with --buildid-tool and --action-id-env",
			buildIDFlags{
				Placement:      buildIDInLdflags,
				HashEnv:        []string{"CC"},
				ActionIDEnv:    actionIDEnv,
				MainExport:     "main.x",
				WriteContentID: true,
				BuildIDTool:    tool,
			},
			nil,
		},
	} {
		err := checkBuildIDFlags(tc.flags)
		if tc.want == nil {
			if err != nil {
				t.Errorf("%s: checkBuildIDFlags = %v, want nil", tc.name, err)
			}
			continue
		}
		var missing *missingFlagError
		if !errors.As(err, &missing) || !slices.Equal(missing.Flags, tc.want.Flags) || missing.Needs != tc.want.Needs {
			t.Errorf("%s: checkBuildIDFlags = %v; want a *missingFlagError with Flags %q, Needs %q", tc.name, err, tc.want.Flags, tc.want.Needs)
		}
	}
}

func TestToolArgsHashesEnvValuesBeforeCwdExpansion(t *testing.T) {
	metadata := writeActionMetadata(t)
	args := []string{"-o", "out", "./cmd"}
	env := map[string]string{"GOOS": "linux", "CC": "%cwd%/cc"}
	buildIDArg := func(hashEnv []string, cwd string) string {
		t.Helper()
		got, err := toolArgs(args, buildIDInputs{MetadataPath: metadata, Env: env, HashEnv: hashEnv, Placement: buildIDLeadingArg}, cwd)
		if err != nil {
			t.Fatal(err)
		}
		return got[0]
	}
	underA, underB, unhashed := buildIDArg([]string{"CC"}, "/a"), buildIDArg([]string{"CC"}, "/b"), buildIDArg(nil, "/a")
	if underA != underB {
		t.Errorf("hashing CC=%%cwd%%/cc: %q under /a, %q under /b; want them equal", underA, underB)
	}
	if underA == unhashed {
		t.Errorf("hashing CC=%%cwd%%/cc gives %q, the same as hashing no variable; want an ID that depends on CC", underA)
	}
}

func TestChildEnvExpandsCwdInHashEnvVariables(t *testing.T) {
	env := map[string]string{
		"CC":         "%cwd%/cc",
		"CGO_CFLAGS": "-I%cwd%/include -ffile-prefix-map=%cwd%=.",
		"GOFLAGS":    "-mod=vendor",
		"UNHASHED":   "%cwd%/x",
	}
	want := map[string]string{
		"CC":         "/a/cc",
		"CGO_CFLAGS": "-I/a/include -ffile-prefix-map=/a=.",
		"GOFLAGS":    "-mod=vendor",
		"UNHASHED":   "%cwd%/x",
	}
	list := childEnv(env, []string{"CC", "CGO_CFLAGS", "GOFLAGS"}, "/a")
	got := make(map[string]string, len(list))
	for _, entry := range list {
		key, value, _ := strings.Cut(entry, "=")
		got[key] = value
	}
	if !maps.Equal(got, want) || len(list) != len(want) {
		t.Errorf("childEnv = %q, want the entries of %q", list, want)
	}
}

// fakeBuildIDTool writes a shell script that stands in for `go tool
// buildid`. The script writes its arguments, one per line, to the file the
// second result names, prints stdout and exits with status.
func fakeBuildIDTool(t *testing.T, stdout string, status int) (tool, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	tool = filepath.Join(dir, "buildid")
	argsFile = filepath.Join(dir, "args")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > '%s'\nprintf '%%s' '%s'\nexit %d\n",
		argsFile, strings.ReplaceAll(stdout, "'", `'\''`), status)
	if err := os.WriteFile(tool, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return tool, argsFile
}

func readArgs(t *testing.T, argsFile string) []string {
	t.Helper()
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func TestReadBuildID(t *testing.T) {
	const id = "AAAAAAAAAAAAAAAAAAAA/bbbbbbbbbbbbbbbbbbbb"
	for _, tc := range []struct {
		name    string
		stdout  string
		status  int
		want    string
		wantErr bool
	}{
		{"prints the ID", id + "\n", 0, id, false},
		{"pads the ID with spaces", "  " + id + " \n", 0, id, false},
		{"prints an empty line", "\n", 0, "", true},
		{"exits with status 1", "", 1, "", true},
	} {
		tool, argsFile := fakeBuildIDTool(t, tc.stdout, tc.status)
		file := filepath.Join(t.TempDir(), "pkg.a")
		got, err := readBuildID(tool, file)
		if got != tc.want || (err != nil) != tc.wantErr {
			t.Errorf("%s: readBuildID = %q, %v; want %q, error %v", tc.name, got, err, tc.want, tc.wantErr)
		}
		if args := readArgs(t, argsFile); !slices.Equal(args, []string{file}) {
			t.Errorf("%s: buildid arguments = %q, want %q", tc.name, args, []string{file})
		}
	}
}

func TestReadBuildIDRejectsAMissingTool(t *testing.T) {
	if id, err := readBuildID(filepath.Join(t.TempDir(), "buildid"), "pkg.a"); err == nil {
		t.Errorf("readBuildID = %q, nil; want an error", id)
	}
}

func TestWriteContentID(t *testing.T) {
	for _, tc := range []struct {
		status  int
		wantErr bool
	}{
		{0, false},
		{1, true},
	} {
		tool, argsFile := fakeBuildIDTool(t, "", tc.status)
		file := filepath.Join(t.TempDir(), "main")
		if err := writeContentID(tool, file); (err != nil) != tc.wantErr {
			t.Errorf("exit status %d: writeContentID = %v, want error %v", tc.status, err, tc.wantErr)
		}
		if args, want := readArgs(t, argsFile), []string{"-w", file}; !slices.Equal(args, want) {
			t.Errorf("exit status %d: buildid arguments = %q, want %q", tc.status, args, want)
		}
	}
}

// realBuildIDTool is the absolute path of the cmd/buildid binary that BUCK
// passes in GO_BUILDID_TOOL. It resolves before the generated test main
// changes into the test binary's directory.
var realBuildIDTool = func() string {
	path, err := filepath.Abs(os.Getenv("GO_BUILDID_TOOL"))
	if err != nil {
		panic(err)
	}
	return path
}()

// When cmd/buildid sets the content ID of a file in the raw build ID form,
// readBuildID returns the action ID unchanged and, as the content ID, the
// hash of the file with its build ID zeroed.
func TestContentIDRoundTripsThroughCmdBuildID(t *testing.T) {
	if os.Getenv("GO_BUILDID_TOOL") == "" {
		t.Fatal("GO_BUILDID_TOOL is unset; BUCK sets it to the cmd/buildid binary")
	}
	raw := func(id string) []byte {
		return []byte("header\n\xff Go build ID: \"" + id + "\"\n \xff\nbody\n")
	}
	const actionPart = "AAAAAAAAAAAAAAAAAAAA/"
	id := actionPart + strings.Repeat("b", 20)
	file := writeTempFile(t, "pkg.o", raw(id))

	if err := writeContentID(realBuildIDTool, file); err != nil {
		t.Fatal(err)
	}
	got, err := readBuildID(realBuildIDTool, file)
	if err != nil {
		t.Fatal(err)
	}
	zeroed := sha256.Sum256(raw(strings.Repeat("\x00", len(id))))
	if want := actionPart + base64.RawURLEncoding.EncodeToString(zeroed[:15]); got != want {
		t.Errorf("build ID after writeContentID = %q, want %q", got, want)
	}
}
