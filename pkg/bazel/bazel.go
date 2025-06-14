/*
 * Copyright 2022 Aspect Build Systems, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package bazel

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/aspect-build/aspect-cli/bazel/analysis"
	"github.com/aspect-build/aspect-cli/bazel/flags"
	"github.com/aspect-build/aspect-cli/pkg/aspecterrors"
	"github.com/aspect-build/aspect-cli/pkg/bazel/workspace"
	"github.com/aspect-build/aspect-cli/pkg/ioutils"
	"github.com/aspect-build/aspect-cli/pkg/ioutils/cache"
	"github.com/spf13/cobra"

	"github.com/bazelbuild/bazelisk/config"
	"github.com/bazelbuild/bazelisk/core"
	"github.com/bazelbuild/bazelisk/repositories"
	"google.golang.org/protobuf/proto"
)

var workingDirectory string

// Global mutable state!
// This is for performance, avoiding a lookup of the possible startup flags for every
// instance of a bazel struct.
// We know the flags will be constant for the lifetime of an `aspect` cli execution.
var allFlags map[string]*flags.FlagInfo

// Global mutable state!
// This is for performance, avoiding needing to set the specified startup flags for every
// instance of a bazel struct.
// We know the specified startup flags will be constant for the lifetime of an `aspect`
// cli execution.
var startupFlags []string

type Bazel interface {
	WithEnv(env []string) Bazel
	AQuery(expr string, bazelFlags []string) (*analysis.ActionGraphContainer, error)
	BazelDashDashVersion() (string, error)
	BazelFlagsAsProto() ([]byte, error)
	HandleReenteringAspect(streams ioutils.Streams, args []string, aspectLockVersion bool) (bool, error)
	RunCommand(streams ioutils.Streams, wd *string, command ...string) error
	InitializeBazelFlags() error
	IsBazelFlag(command string, flag string) (bool, error)
	Flags() (map[string]*flags.FlagInfo, error)
	AbsPathRelativeToWorkspace(relativePath string) (string, error)
	AddBazelFlags(cmd *cobra.Command) error
	WorkspaceRoot() string
	MakeBazelCommand(ctx context.Context, args []string, streams ioutils.Streams, env []string, wd *string) (*exec.Cmd, error)
}

type bazel struct {
	workspaceRoot string
	env           []string
}

// ExecutablePath implements Bazel.
func (b *bazel) MakeBazelCommand(ctx context.Context, args []string, streams ioutils.Streams, env []string, wd *string) (*exec.Cmd, error) {
	bazelisk := NewBazelisk(b.workspaceRoot, false)
	repos := createRepositories(bazelisk.config)
	bazelInstallation, err := bazelisk.GetBazelInstallation(repos, bazelisk.config)
	if err != nil {
		return nil, fmt.Errorf("could not get path to Bazel: %v", err)
	}
	allArgs := []string{}
	allArgs = append(allArgs, startupFlags...)
	allArgs = append(allArgs, args...)
	return bazelisk.makeBazelCmd(bazelInstallation.Path, allArgs, streams, env, bazelisk.config, wd, ctx), nil
}

// WorkspaceRoot implements Bazel.
func (b *bazel) WorkspaceRoot() string {
	return b.workspaceRoot
}

func New(workspaceRoot string) Bazel {
	// If we are given a non-empty workspace root, make sure that it is an absolute path. We support
	// an empty workspace root for Bazel commands that support being run outside of a workspace
	// (e.g. version).
	absWkspRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		// If we can't get the absolute path, it's a programming logic error.
		panic(err)
	}

	return &bazel{
		workspaceRoot: absWkspRoot,
	}
}

// This is a special case where we run Bazel without a workspace (e.g., version).
var NoWorkspaceRoot Bazel = &bazel{}

var WorkspaceFromWd Bazel = findWorkspace()

func findWorkspace() Bazel {
	if workingDirectory == "" {
		wd, err := os.Getwd()
		if err != nil {
			panic(err)
		}
		workingDirectory = wd
	}
	finder := workspace.DefaultFinder
	wr, err := finder.Find(workingDirectory)
	if err != nil {
		return NoWorkspaceRoot
	}
	return New(wr)
}

func getLastLine(input string) string {
	// Split the string by newline
	lines := strings.Split(input, "\n")

	// Return the last non-empty line
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i] != "" {
			return lines[i]
		}
	}

	// If all lines are empty, return an empty string
	return ""
}

func stripColorCodes(input string) string {
	// Regular expression to match ANSI escape codes
	re := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	// Replace all occurrences of the escape codes with an empty string
	return re.ReplaceAllString(input, "")
}

func (b *bazel) WithEnv(env []string) Bazel {
	b.env = env
	return b
}

func createRepositories(config config.Config) *core.Repositories {
	gcs := &repositories.GCSRepo{}
	gitHub := repositories.CreateGitHubRepo(config.Get("BAZELISK_GITHUB_TOKEN"))
	// Fetch LTS releases & candidates, rolling releases and Bazel-at-commits from GCS, forks from GitHub.
	return core.CreateRepositories(gcs, gitHub, gcs, gcs, true)
}

func scrubEnvOfBazeliskAspectBootstrap() {
	bazeliskConfig := &bazeliskVersionConfig{
		UseBazelVersion: os.Getenv(useBazelVersionEnv),
		BazeliskBaseUrl: os.Getenv(core.BaseURLEnv),
	}
	if isBazeliskAspectBootstrap(bazeliskConfig) {
		os.Setenv(useBazelVersionEnv, "")
		os.Setenv(core.BaseURLEnv, "")
	}
}

// Check if we should re-enter a different version and/or tier of the Aspect CLI and re-enter if we should.
// Error is returned if version and/or tier are misconfigured in the Aspect CLI config.
func (b *bazel) HandleReenteringAspect(streams ioutils.Streams, args []string, aspectLockVersion bool) (bool, error) {
	bazelisk := NewBazelisk(b.workspaceRoot, true)

	// Calling bazelisk.getBazelVersionAndUrl() has the side-effect of setting AspectShouldReenter.
	// TODO: this pattern could get cleaned up so it does not rely on the side-effect
	_, _, err := bazelisk.getBazelVersionAndUrl()
	if err != nil {
		return false, err
	}

	if bazelisk.AspectShouldReenter && !aspectLockVersion {
		repos := createRepositories(bazelisk.config)
		err := bazelisk.Run(args, repos, streams, b.env, bazelisk.config, nil)
		return true, err
	}

	return false, nil
}

func GetAspectVersions() ([]string, error) {
	return GetBazelVersions("aspect-build/aspect-cli")
}

func GetBazelVersions(bazelFork string) ([]string, error) {
	repos := createRepositories(core.MakeDefaultConfig())

	aspectCacheDir, err := cache.AspectCacheDir()
	if err != nil {
		return nil, err
	}

	return repos.Fork.GetVersions(aspectCacheDir, bazelFork)
}

func (b *bazel) RunCommand(streams ioutils.Streams, wd *string, command ...string) error {
	// Prepend startup flags
	command = append(startupFlags, command...)

	bazelisk := NewBazelisk(b.workspaceRoot, false)
	repos := createRepositories(bazelisk.config)
	return bazelisk.Run(command, repos, streams, b.env, bazelisk.config, wd)
}

// Initializes start-up flags from args and returns args without start-up flags
func InitializeStartupFlags(args []string) ([]string, []string, error) {
	nonFlags, flags, err := SeparateBazelFlags("startup", args)
	if err != nil {
		return nil, nil, err
	}
	startupFlags = flags
	return nonFlags, flags, nil
}

// Flags fetches the metadata for Bazel's command line flag via `bazel help flags-as-proto`
func (b *bazel) Flags() (map[string]*flags.FlagInfo, error) {
	if allFlags != nil {
		return allFlags, nil
	}

	aspectCacheDir, err := cache.AspectCacheDir()
	if err != nil {
		return nil, err
	}

	flagsAsProtoCacheDir := path.Join(aspectCacheDir, "cli-flags-proto-cache")
	err = os.MkdirAll(flagsAsProtoCacheDir, os.ModePerm)
	if err != nil {
		return nil, fmt.Errorf("failed write create directory %s: %w", flagsAsProtoCacheDir, err)
	}

	bazelisk := NewBazelisk(b.workspaceRoot, false)
	repos := createRepositories(bazelisk.config)
	bazelInstallation, err := bazelisk.GetBazelInstallation(repos, bazelisk.config)
	if err != nil {
		return nil, fmt.Errorf("failed to get path to Bazel: %w", err)
	}

	bazelPath := bazelInstallation.Path

	// If bazelPath ends in local/something/bin/bazel then don't cache since it is a local bazel
	// that could change versions. Search for "linkLocalBazel" function in the code to find where
	// that is set.
	localBazelRegex := regexp.MustCompile(`\/local\/[^\/]+\/bin\/bazel$`)
	localBazel := localBazelRegex.MatchString(bazelPath)

	var flagsProtoCache string
	var flagsProtoBytes []byte
	flagCollection := &flags.FlagCollection{}

	// Read flagsProtoBytes from the flagsProtoCache if bazel is not configured to a local path
	if !localBazel {
		h := sha1.New()
		h.Write([]byte(bazelPath))
		bazelPathHash := hex.EncodeToString(h.Sum(nil))

		flagsProtoCache = path.Join(flagsAsProtoCacheDir, bazelPathHash)
		flagsProtoBytes, err = os.ReadFile(flagsProtoCache)
		if err == nil {
			err = proto.Unmarshal(flagsProtoBytes, flagCollection)
			if err != nil {
				fmt.Fprintf(os.Stderr, "failed to parse flags proto cache file %s: %v\n", flagsProtoCache, err)
				os.Remove(flagsProtoCache)
				flagsProtoBytes = nil
			}
		} else if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "failed to read flags proto cache file %s: %v\n", flagsProtoCache, err)
			os.Remove(flagsProtoCache)
			flagsProtoBytes = nil
		}
	}

	// If we have not read the flagsProtoBytes from the flagsProtoCache file then we fetch the
	// data from bazel
	if flagsProtoBytes == nil {
		flagsProtoBytes, err = b.BazelFlagsAsProto()
		if err != nil {
			return nil, fmt.Errorf("failed to fetch bazel flags: %w", err)
		}
		if err = proto.Unmarshal(flagsProtoBytes, flagCollection); err != nil {
			return nil, fmt.Errorf("failed to unmarshal bazel flags: %w", err)
		}
		// Write to the flagsProtoCache if bazel is not configured to a local path
		if !localBazel {
			err = os.WriteFile(flagsProtoCache, flagsProtoBytes, 0644)
			if err != nil {
				return nil, fmt.Errorf("failed write flags proto cache file : %w", err)
			}
		}
	}

	allFlags = make(map[string]*flags.FlagInfo)

	for i := range flagCollection.FlagInfos {
		allFlags[*flagCollection.FlagInfos[i].Name] = flagCollection.FlagInfos[i]
	}

	return allFlags, nil
}

// AQuery runs a `bazel aquery` command and returns the resulting parsed proto data.
func (b *bazel) AQuery(query string, bazelFlags []string) (*analysis.ActionGraphContainer, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	streams := ioutils.Streams{
		Stdin:  os.Stdin,
		Stdout: &stdout,
		Stderr: &stderr,
	}

	bazelErrs := make(chan error, 1)
	defer close(bazelErrs)

	go func() {
		cmd := []string{"aquery"}
		cmd = append(cmd, bazelFlags...)
		cmd = append(cmd, "--output=proto")

		if query != "" {
			cmd = append(cmd, "--")
			cmd = append(cmd, query)
		}
		err := b.RunCommand(streams, nil, cmd...)
		bazelErrs <- err
	}()

	err := <-bazelErrs

	if err != nil {
		var exitErr *aspecterrors.ExitError
		if errors.As(err, &exitErr) {
			// Dump the `stderr` when Bazel executed and exited non-zero
			return nil, fmt.Errorf("failed to run aquery: %w\nstderr:\n%s", err, stderr.String())
		} else {
			return nil, fmt.Errorf("failed to run aquery: %w", err)
		}
	}

	agc := &analysis.ActionGraphContainer{}

	protoBytes, err := io.ReadAll(&stdout)
	if err != nil {
		return nil, fmt.Errorf("failed to run aquery: %w", err)
	}

	proto.Unmarshal(protoBytes, agc)
	if err := proto.Unmarshal(protoBytes, agc); err != nil {
		return nil, fmt.Errorf("failed to run Bazel aquery: parsing ActionGraphContainer: %w", err)
	}
	return agc, nil
}

// Calls `bazel --version` and returns the result
func (b *bazel) BazelDashDashVersion() (string, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	streams := ioutils.Streams{
		Stdin:  os.Stdin,
		Stdout: &stdout,
		Stderr: &stderr,
	}

	bazelErrs := make(chan error, 1)
	defer close(bazelErrs)

	go func() {
		cmd := []string{"--version"}
		err := b.RunCommand(streams, nil, cmd...)
		bazelErrs <- err
	}()

	err := <-bazelErrs

	if err != nil {
		var exitErr *aspecterrors.ExitError
		if errors.As(err, &exitErr) {
			// Dump the `stderr` when Bazel executed and exited non-zero
			return "", fmt.Errorf("failed to run bazel --version: %w\nstderr:\n%s", err, stderr.String())
		} else {
			return "", fmt.Errorf("failed to run bazel --version: %w", err)
		}
	}

	version, err := io.ReadAll(&stdout)
	if err != nil {
		return "", fmt.Errorf("failed to run bazel --version: %w", err)
	}

	return string(version[:]), nil
}

func (b *bazel) AbsPathRelativeToWorkspace(relativePath string) (string, error) {
	if b.workspaceRoot == "" {
		return "", errors.New("the bazel instance does not have a workspace root")
	}
	if filepath.IsAbs(relativePath) {
		return relativePath, nil
	}
	return filepath.Join(b.workspaceRoot, relativePath), nil
}

// Calls `bazel help flags-as-proto` in a sandboxed WORKSPACE and returns the result
func (b *bazel) BazelFlagsAsProto() ([]byte, error) {
	// create a directory in the aspect cache dir with an empty WORKSPACE file to run
	// `bazel help flags-as-proto` in so it doesn't affect the bazel server in the user's WORKSPACE
	aspectCacheDir, err := cache.AspectCacheDir()
	if err != nil {
		return nil, err
	}

	tmpdir := path.Join(aspectCacheDir, "cli-flags-proto-wksp")
	err = os.MkdirAll(tmpdir, os.ModePerm)
	if err != nil {
		return nil, fmt.Errorf("failed write create directory %s: %w", tmpdir, err)
	}
	err = os.WriteFile(path.Join(tmpdir, "WORKSPACE.bazel"), []byte{}, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed write WORKSPACE.bazel file in %s: %w", tmpdir, err)
	}
	err = os.WriteFile(path.Join(tmpdir, "WORKSPACE.bzlmod"), []byte{}, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed write WORKSPACE.bzlmod file in %s: %w", tmpdir, err)
	}
	err = os.WriteFile(path.Join(tmpdir, "MODULE.bazel"), []byte{}, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed write MODULE.bazel file in %s: %w", tmpdir, err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	streams := ioutils.Streams{
		Stdin:  os.Stdin,
		Stdout: &stdout,
		Stderr: &stderr,
	}

	bazelErrs := make(chan error, 1)
	defer close(bazelErrs)

	go func(wd string) {
		// Running in batch mode will prevent bazel from spawning a daemon. Spawning a bazel daemon takes time which is something we don't want here.
		// Also, instructing bazel to ignore all rc files will protect it from failing if any of the rc files is broken.
		err := b.RunCommand(streams, &wd, "--batch", "--ignore_all_rc_files", "help", "flags-as-proto")
		bazelErrs <- err
	}(tmpdir)

	err = <-bazelErrs

	if err != nil {
		var exitErr *aspecterrors.ExitError
		if errors.As(err, &exitErr) {
			// Dump the `stderr` when Bazel executed and exited non-zero
			return nil, fmt.Errorf("failed to get bazel flags (running in %s): %w\nstderr:\n%s", tmpdir, err, stderr.String())
		} else {
			return nil, fmt.Errorf("failed to get bazel flags: %w", err)
		}
	}

	flagsProtoBase64, err := io.ReadAll(&stdout)
	if err != nil {
		return nil, fmt.Errorf("failed to read bazel flags: %w", err)
	}

	// getLastLine & stripColorCodes are work-arounds for the BB CLI which mixes stderr into stdout in its output
	// and also adds color codes:
	// Extracting Bazel installation...
	// Starting local Bazel server and connecting to it...
	// Cs8CCiVleHBlcmltZW50...XTkoGUkVNT1RFUAA=[0m
	flagsProtoBase64Sanitized := stripColorCodes(getLastLine(string(flagsProtoBase64[:])))

	flagsProtoBytes, err := base64.StdEncoding.DecodeString(flagsProtoBase64Sanitized)
	if err != nil {
		return nil, fmt.Errorf("failed to decode bazel flags: %w\n==> %v", err, flagsProtoBase64Sanitized)
	}

	return flagsProtoBytes, nil
}

type Output struct {
	Label    string
	Mnemonic string
	Path     string
}

// ParseOutputs reads the proto result of AQuery and extracts the output file paths with their generator mnemonics.
func ParseOutputs(agc *analysis.ActionGraphContainer) []Output {
	// Use RAM to store lookup maps for these identifiers
	// rather than an O(n^2) algorithm of searching on each access.
	frags := make(map[uint32]*analysis.PathFragment)
	for _, f := range agc.PathFragments {
		frags[f.Id] = f
	}
	arts := make(map[uint32]*analysis.Artifact)
	for _, a := range agc.Artifacts {
		arts[a.Id] = a
	}
	targets := make(map[uint32]*analysis.Target)
	for _, t := range agc.Targets {
		targets[t.Id] = t
	}

	// The paths in the proto data are organized as a trie
	// to make the representation more compact.
	// https://en.wikipedia.org/wiki/Trie
	// Make a map to store each prefix so we can memoize common paths
	prefixes := make(map[uint32]*[]string)

	// Declare a recursive function to walk up the trie to the root.
	var prefix func(pathID uint32) []string

	prefix = func(pathID uint32) []string {
		if prefixes[pathID] != nil {
			return *prefixes[pathID]
		}
		fragment := frags[pathID]
		// Reconstruct the path from the parent pointers.
		segments := []string{fragment.Label}

		if fragment.ParentId > 0 {
			segments = append(segments, prefix(fragment.ParentId)...)
		}
		prefixes[pathID] = &segments
		return segments
	}

	var result []Output
	for _, a := range agc.Actions {
		for _, i := range a.OutputIds {
			artifact := arts[i]
			segments := prefix(artifact.PathFragmentId)
			var path strings.Builder
			// Assemble in reverse order.
			for i := len(segments) - 1; i >= 0; i-- {
				path.WriteString(segments[i])
				if i > 0 {
					path.WriteString("/")
				}
			}
			result = append(result, Output{
				targets[a.TargetId].Label,
				a.Mnemonic,
				path.String(),
			})
		}
	}
	return result
}
