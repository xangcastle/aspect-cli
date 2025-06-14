/*
 * Copyright 2023 Aspect Build Systems, Inc.
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

package lint

import (
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/aspect-build/aspect-cli/bazel/buildeventstream"
)

// ResultForLabelAndMnemonic aggregates the relevant files we find in the BEP for
type ResultForLabelAndMnemonic struct {
	label        string
	mnemonic     string
	exitCodeFile *buildeventstream.File
	reportFile   *buildeventstream.File
	patchFile    *buildeventstream.File
}

type LintBEPHandler struct {
	namedSets                map[string]*buildeventstream.NamedSetOfFiles
	workspaceRoot            string
	localExecRoot            string
	besCompleted             chan<- struct{}
	resultsByLabelByMnemonic map[string]*ResultForLabelAndMnemonic
}

type OrderedBuildEvent struct {
	event          *buildeventstream.BuildEvent
	sequenceNumber int64
}

func newLintBEPHandler(workspaceRoot string, besCompleted chan<- struct{}) *LintBEPHandler {
	return &LintBEPHandler{
		namedSets:                make(map[string]*buildeventstream.NamedSetOfFiles),
		resultsByLabelByMnemonic: make(map[string]*ResultForLabelAndMnemonic),
		workspaceRoot:            workspaceRoot,
		besCompleted:             besCompleted,
	}
}

func (runner *LintBEPHandler) readBEPFile(file *buildeventstream.File) ([]byte, error) {
	resultsFile := ""

	switch f := file.File.(type) {
	case *buildeventstream.File_Uri:
		uri, err := url.Parse(f.Uri)
		if err != nil {
			return nil, fmt.Errorf("unable to parse URI %s: %v", f.Uri, err)
		}
		if uri.Scheme == "file" {
			resultsFile = filepath.Clean(uri.Path)
		} else if uri.Scheme == "bytestream" {
			if strings.HasSuffix(uri.Path, "/0") {
				// No reason to read an empty results file from disk
				return nil, nil
			}
			// Because we set --experimental_remote_download_regex, we can depend on the results file being
			// in the output tree even when using a remote cache with build without the bytes.
			// If possible, we use the localExecRoot from the workspaceInfo event when constructing the path
			// to the results file in case the convenience symlinks are not present, e.g. if
			// --experimental_convenience_symlinks=ignore is specified.
			root := runner.workspaceRoot
			if runner.localExecRoot != "" {
				root = runner.localExecRoot
			}
			resultsFile = path.Join(root, path.Join(file.PathPrefix...), file.Name)
		} else {
			return nil, fmt.Errorf("unsupported BES file uri %v", f.Uri)
		}
	default:
		return nil, fmt.Errorf("unsupported BES file type")
	}

	start := time.Now()
	for {
		// TODO: also check that the bazel remote cache downloader is not still writing
		// to the results file
		_, err := os.Stat(resultsFile)
		if err != nil {
			// if more than 60s has passed then give up
			// TODO: make this timeout configurable
			if time.Since(start) > 60*time.Second {
				return nil, fmt.Errorf("failed to find lint results file %s: %v", resultsFile, err)
			}
		} else {
			buf, err := os.ReadFile(resultsFile)
			if err != nil {
				return nil, fmt.Errorf("failed to read lint results file %s: %v", resultsFile, err)
			}
			return buf, nil
		}
		// we're in a go routine so yield for 100ms and try again
		// TODO: watch the file system for the file creation instead of polling
		t := time.NewTimer(time.Millisecond * 100)
		<-t.C
	}
}

func parseLinterMnemonicFromFilename(filename string) string {
	// Parse the filename convention that rules_lint has for output files.
	// path/to/<target_name>.<mnemonic>.<suffix> -> linter
	// See https://github.com/aspect-build/rules_lint/blob/6df14f0e5dae0c9a9c0e8e6f69e25bbdb3aa7394/lint/private/lint_aspect.bzl#L28.
	s := strings.Split(filepath.Base(filename), ".")
	if len(s) < 3 {
		return ""
	}
	// Filter out mnemonics that don't start with AspectRulesLint, which is the rules_lint convention
	if !strings.HasPrefix(s[len(s)-2], "AspectRulesLint") {
		return ""
	}
	return s[len(s)-2]
}

func (runner *LintBEPHandler) bepEventCallback(event *buildeventstream.BuildEvent, sn int64) error {
	switch event.Payload.(type) {

	case *buildeventstream.BuildEvent_WorkspaceInfo:
		runner.localExecRoot = event.GetWorkspaceInfo().GetLocalExecRoot()

	case *buildeventstream.BuildEvent_NamedSetOfFiles:
		runner.namedSets[event.Id.GetNamedSet().Id] = event.GetNamedSetOfFiles()

	case *buildeventstream.BuildEvent_Completed:
		label := event.Id.GetTargetCompleted().GetLabel()

		for _, outputGroup := range event.GetCompleted().OutputGroup {
			for _, fileSetId := range outputGroup.FileSets {
				if fileSet := runner.namedSets[fileSetId.Id]; fileSet != nil {
					runner.namedSets[fileSetId.Id] = nil
					result := &ResultForLabelAndMnemonic{label: label}

					for _, file := range fileSet.GetFiles() {
						if outputGroup.Name == LINT_PATCH_GROUP {
							if mnemonic := parseLinterMnemonicFromFilename(file.Name); mnemonic != "" {
								result.mnemonic = mnemonic

								savedResult := runner.resultsByLabelByMnemonic[label+mnemonic]
								if savedResult == nil {
									runner.resultsByLabelByMnemonic[label+mnemonic] = result
								} else {
									result = savedResult
								}
							}
							result.patchFile = file
						} else if outputGroup.Name == LINT_REPORT_GROUP_MACHINE {
							if mnemonic := parseLinterMnemonicFromFilename(file.Name); mnemonic != "" {
								result.mnemonic = mnemonic

								savedResult := runner.resultsByLabelByMnemonic[label+mnemonic]
								if savedResult == nil {
									runner.resultsByLabelByMnemonic[label+mnemonic] = result
								} else {
									result = savedResult
								}
							}
							if strings.HasSuffix(file.Name, ".report") {
								result.reportFile = file
							} else if strings.HasSuffix(file.Name, ".exit_code") {
								result.exitCodeFile = file
							}
						} else if outputGroup.Name == LINT_REPORT_GROUP_HUMAN {
							if mnemonic := parseLinterMnemonicFromFilename(file.Name); mnemonic != "" {
								result.mnemonic = mnemonic

								savedResult := runner.resultsByLabelByMnemonic[label+mnemonic]
								if savedResult == nil {
									runner.resultsByLabelByMnemonic[label+mnemonic] = result
								} else {
									result = savedResult
								}
							}
							if strings.HasSuffix(file.Name, ".out") {
								result.reportFile = file
							} else if strings.HasSuffix(file.Name, ".exit_code") {
								result.exitCodeFile = file
							}
						}
					}
				}
			}
		}

	case *buildeventstream.BuildEvent_Finished:
		// signal that the BES build finished event has been received and we're done processing lint
		// result files from the BEP; we should only receive this event once but clear the channel
		// out to be safe
		if runner.besCompleted != nil {
			runner.besCompleted <- struct{}{}
			close(runner.besCompleted)
			runner.besCompleted = nil
		}
	}

	return nil
}
