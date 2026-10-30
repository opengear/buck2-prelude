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
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func loadArgs(args []string) []string {
	newArgs := make([]string, 0, 0)
	for _, arg := range args {
		if !strings.HasPrefix(arg, "@") {
			newArgs = append(newArgs, arg)
		} else {
			file, _ := os.Open(arg[1:])
			defer file.Close()
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				newArgs = append(newArgs, scanner.Text())
			}
		}
	}
	return newArgs
}

func jsonStreamToArray(r io.Reader, w io.Writer) error {
	var objs []any
	for dec := json.NewDecoder(r); dec.More(); {
		var obj any
		if err := dec.Decode(&obj); err != nil {
			return fmt.Errorf("failed to decode json: %w", err)
		}
		objs = append(objs, obj)
	}
	if err := json.NewEncoder(w).Encode(objs); err != nil {
		return fmt.Errorf("failed to encode json: %w", err)
	}
	return nil
}

func main() {
	os.Args = loadArgs(os.Args)
	var wrappedBinary = flag.String("go", "", "wrapped go binary")
	var goRoot = flag.String("goroot", "", "go root")
	var defaultGoOS = flag.String("default-goos", "", "default GOOS (if not set by env)")
	var defaultGoArch = flag.String("default-goarch", "", "default GOARCH (if not set by env)")
	var outputFile = flag.String("output", "", "file to redirect stdout to")
	var convertJsonStream = flag.Bool("convert-json-stream", false, "convert json stream to array")
	var gnuBuildID = flag.Bool("gnu-build-id", false, "rewrite .note.gnu.build-id with sha256 of the linked output (internal Go links only)")
	var actionIDEnv = flag.String("action-id-env", "", "environment variable that holds the path of Buck2's action metadata. The wrapper passes the tool a -buildid. Its action ID is a hash of the metadata's input digests, the tool's arguments, the Go build variables and the --hash-env variables")
	var buildIDTool = flag.String("buildid-tool", "", "path of the cmd/buildid binary; --main-export and --write-content-id run it")
	var mainExport = flag.String("main-export", "", "export file of the main package in a link; the link's -buildid puts that file's build ID between two copies of the link's action ID")
	var writeContentIDFlag = flag.Bool("write-content-id", false, "after the wrapped tool succeeds, cmd/buildid -w writes the -o output's content ID, the last part of its build ID")
	var buildIDLdflags = flag.Bool("buildid-ldflags", false, "put -buildid at the front of the go build -ldflags value, which go build passes to its link. Needs --action-id-env. The go build arguments must hold exactly one -ldflags, and its value must lack a -buildid and must not take the per-package pattern=flags form")
	var hashEnv []string
	flag.Func("hash-env", "environment variable `NAME` whose value the wrapper hashes into the action ID with %cwd% unexpanded, then passes to the tool with %cwd% expanded. Repeat the flag for each variable. Each must be set, and the flag needs --action-id-env", func(name string) error {
		hashEnv = append(hashEnv, name)
		return nil
	})
	flag.Parse()
	unknownArgs := flag.Args()

	if *wrappedBinary == "" {
		log.Fatal("No wrapped binary specified")
	}
	checked := buildIDFlags{
		Placement:      buildIDLeadingArg,
		HashEnv:        hashEnv,
		ActionIDEnv:    *actionIDEnv,
		MainExport:     *mainExport,
		WriteContentID: *writeContentIDFlag,
		BuildIDTool:    *buildIDTool,
	}
	if *buildIDLdflags {
		checked.Placement = buildIDInLdflags
	}
	if err := checkBuildIDFlags(checked); err != nil {
		log.Fatalf("Parsing flags: %v", err)
	}

	absWrappedBinary, err := filepath.Abs(*wrappedBinary)
	if err != nil {
		log.Fatalf("Resolving wrapped binary %q: %v", *wrappedBinary, err)
	}

	envs := make(map[string]string)
	for _, e := range os.Environ() {
		pair := strings.SplitN(e, "=", 2)
		envs[pair[0]] = pair[1]
	}

	goroot := *goRoot
	if goroot == "" {
		goroot = envs["GOROOT"]
	}

	if os.Getenv("GOOS") == "" && *defaultGoOS != "" {
		envs["GOOS"] = *defaultGoOS
	}
	if os.Getenv("GOARCH") == "" && *defaultGoArch != "" {
		envs["GOARCH"] = *defaultGoArch
	}

	if goroot != "" {
		absGoroot, err := filepath.Abs(goroot)
		if err != nil {
			log.Fatalf("Resolving GOROOT %q: %v", goroot, err)
		}
		envs["GOROOT"] = absGoroot
	}

	if buckScratchPath, ok := envs["BUCK_SCRATCH_PATH"]; ok {
		absBuckScratchPath, err := filepath.Abs(buckScratchPath)
		if err != nil {
			log.Fatalf("Resolving BUCK_SCRATCH_PATH %q: %v", buckScratchPath, err)
		}
		envs["GOCACHE"] = absBuckScratchPath
		envs["TMPDIR"] = absBuckScratchPath
	}

	metadataPath := ""
	if *actionIDEnv != "" {
		metadataPath = envs[*actionIDEnv]
		if metadataPath == "" {
			log.Fatalf("Computing action ID: environment variable %q is unset or empty", *actionIDEnv)
		}
	}

	mainBuildID := ""
	if *mainExport != "" {
		var err error
		mainBuildID, err = readBuildID(*buildIDTool, *mainExport)
		if err != nil {
			log.Fatalf("Computing link build ID: %v", err)
		}
	}

	cwd, err := os.Getwd()
	if err != nil {
		log.Fatalf("Getting current working directory: %v", err)
	}

	unknownArgs, err = toolArgs(unknownArgs, buildIDInputs{
		MetadataPath: metadataPath,
		Env:          envs,
		HashEnv:      checked.HashEnv,
		MainBuildID:  mainBuildID,
		Placement:    checked.Placement,
	}, cwd)
	if err != nil {
		log.Fatalf("Composing tool arguments: %v", err)
	}

	var output *os.File
	if *outputFile == "" {
		output = os.Stdout
	} else {
		output, err = os.Create(*outputFile)
		if err != nil {
			log.Fatalf("Creating output file %q: %v", *outputFile, err)
		}
		defer output.Close()
	}

	cmd := exec.Command(absWrappedBinary, unknownArgs...)
	cmd.Env = childEnv(envs, checked.HashEnv, cwd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Fatalf("Creating stdout pipe: %v", err)
	}
	defer stdout.Close()

	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		log.Fatalf("Starting %q: %v", absWrappedBinary, err)
	}

	if *convertJsonStream {
		if err := jsonStreamToArray(stdout, output); err != nil {
			log.Fatalf("Converting JSON stream: %v", err)
		}
	} else {
		if _, err := io.Copy(output, stdout); err != nil {
			log.Fatalf("Copying stdout: %v", err)
		}
	}

	err = cmd.Wait()
	if err != nil {
		exitCode := 1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "Running %q: %v\n", absWrappedBinary, err)
		os.Exit(exitCode)
	}

	if *gnuBuildID || *writeContentIDFlag {
		out := outputPath(unknownArgs)
		if out == "" {
			log.Fatalf("Writing build IDs: the tool arguments %q lack an -o output", unknownArgs)
		}
		if *gnuBuildID {
			if err := setGNUBuildID(out); err != nil {
				log.Fatalf("Setting GNU build ID in %q: %v", out, err)
			}
		}
		if *writeContentIDFlag {
			if err := writeContentID(*buildIDTool, out); err != nil {
				log.Fatalf("Finishing link output %q: %v", out, err)
			}
		}
	}
}
