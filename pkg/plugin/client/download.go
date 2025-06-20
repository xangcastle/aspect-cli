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

// This file is inspired from https://github.com/bazelbuild/bazelisk/blob/c044e9471ed6a69bad1976dafa312200ae811d5e/platforms/platforms.go#L57

package client

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/aspect-build/aspect-cli/pkg/ioutils/cache"
	"github.com/bazelbuild/bazelisk/config"
	"github.com/bazelbuild/bazelisk/httputil"
	"github.com/fatih/color"
)

var faint = color.New(color.Faint)

func DownloadPlugin(url string, name string, version string) (string, error) {
	aspectCacheDir, err := cache.AspectCacheDir()
	if err != nil {
		return "", err
	}

	pluginsCache := filepath.Join(aspectCacheDir, "plugins", name, version)
	err = os.MkdirAll(pluginsCache, 0755)
	if err != nil {
		return "", fmt.Errorf("could not create directory %s: %v", pluginsCache, err)
	}

	filename, err := determinePluginFilename(name)
	if err != nil {
		return "", fmt.Errorf("unable to determine filename to fetch: %v", err)
	}

	versionedURL := fmt.Sprintf("%s/%s/%s", url, version, filename)

	pluginfile, err := downloadBinary(versionedURL, pluginsCache, filename)
	if err != nil {
		return "", fmt.Errorf("unable to fetch remote plugin from %s: %v", url, err)
	}

	// We don't care if this errors. We have logic to do Trust on first use (TOFU).
	downloadBinarySha(versionedURL, pluginsCache, filename)

	return pluginfile, nil
}

// determineBazelFilename returns the correct file name of a local Bazel binary.
// The logic produces the same naming as our /release/release.bzl gives to our aspect-cli binaries.
func determinePluginFilename(pluginName string) (string, error) {
	var machineName string
	switch runtime.GOARCH {
	case "amd64", "arm64":
		machineName = runtime.GOARCH
	default:
		return "", fmt.Errorf("unsupported machine architecture \"%s\", must be arm64 or x86_64", runtime.GOARCH)
	}

	var osName string
	switch runtime.GOOS {
	case "darwin", "linux", "windows":
		osName = runtime.GOOS
	default:
		return "", fmt.Errorf("unsupported operating system \"%s\", must be Linux, macOS or Windows", runtime.GOOS)
	}

	filenameSuffix := ""
	if runtime.GOOS == "windows" {
		filenameSuffix = ".exe"
	}

	return fmt.Sprintf("%s-%s_%s%s", pluginName, osName, machineName, filenameSuffix), nil
}

func downloadBinary(originURL, destDir, destFile string) (string, error) {
	return httputil.DownloadBinary(originURL, destDir, destFile, config.FromEnv())
}

func downloadBinarySha(versionedURL, destDir, destFile string) (string, error) {
	sha256URL := fmt.Sprintf("%s.sha256", versionedURL)
	sha256Filename := fmt.Sprintf("%s.sha256", destFile)

	// Use DownloadBinary() to ensure the same HTTP auth/header logic is used
	p, err := httputil.DownloadBinary(sha256URL, destDir, sha256Filename, config.FromEnv())
	if err != nil {
		return p, err
	}

	if err := os.Chmod(p, 0400); err != nil {
		return p, err
	}

	return p, nil
}
