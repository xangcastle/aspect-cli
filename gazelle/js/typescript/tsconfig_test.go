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

package typescript

import (
	"path"
	"reflect"
	"testing"
)

func assertTrue(t *testing.T, b bool, msg string) {
	if !b {
		t.Error(msg)
	}
}

func assertEqual(t *testing.T, a, b string, msg string) {
	assertTrue(t, a == b, msg+"\n\tactual:   "+a+"\n\texpected: "+b)
}

func parseTest(t *testing.T, configDir, tsconfigJSON string) *TsConfig {
	options, err := parseTsConfigJSON(make(map[string]*TsConfig), identityResolver, ".", path.Join(configDir, "tsconfig.json"), []byte(tsconfigJSON))
	if err != nil {
		t.Fatalf("failed to parse options: %v\n\n%s", err, tsconfigJSON)
	}

	return options
}

func assertExpand(t *testing.T, options *TsConfig, p string, expected ...string) {
	actual := options.ExpandPaths(".", p)

	// TODO: why does reflect.DeepEqual not handle this case?
	if len(actual) == 0 && len(expected) == 0 {
		return
	}

	if !reflect.DeepEqual(actual, expected) {
		t.Errorf("TsConfig ExpandPath(%q):\n\texpected: %v\n\tactual:   %v", p, expected, actual)
	}
}

func TestIsRelativePath(t *testing.T) {
	t.Run("config metadata", func(t *testing.T) {
		subdirOptions := parseTest(t, "sub/dir", "{}")
		assertEqual(t, subdirOptions.ConfigDir, "sub/dir", "ConfigDir")
		assertEqual(t, subdirOptions.ConfigName, "tsconfig.json", "ConfigDir")
	})

	t.Run("relative path strings", func(t *testing.T) {

		shouldNotMatch := []string{
			"example/test",
			"/absolute/path",
			"another/not/relative/path",
			".dotfile",
		}

		for _, s := range shouldNotMatch {
			if isRelativePath(s) {
				t.Errorf("isRelativePath(%s) should not be relative but was matched as it would", s)
			}
		}

	})

	t.Run("not relative path strings", func(t *testing.T) {
		shouldMatch := []string{
			"./path",
			"../parent",
		}

		for _, s := range shouldMatch {
			if !isRelativePath(s) {
				t.Errorf("isRelativePath(%s) should be relative but was NOT matched as it would", s)
			}
		}
	})

}

var _ TsConfigResolver = identityResolver

func identityResolver(dir, conf string) []string {
	return []string{path.Join(dir, conf)}
}

var _ TsConfigResolver = fooResolver

func fooResolver(dir, conf string) []string {
	return []string{
		path.Join(dir, conf),
		path.Join(dir, "base.tsconfig.json"),
	}
}

func TestTsconfigLoad(t *testing.T) {
	t.Run("parse a tsconfig extending other", func(t *testing.T) {
		extender, err := parseTsConfigJSONFile(make(map[string]*TsConfig), identityResolver, ".", "tests/extends-base.json")
		if err != nil {
			t.Errorf("parseTsConfigJSONFile: %v", err)
		}

		if !extender.ImportHelpers {
			t.Errorf("should inherit compilerOptions.importHelpers")
		}
		assertEqual(t, extender.Paths.Rel, "src", "should inherit Paths.Rel from extended")
		assertEqual(t, (*extender.Paths.Map)["alias-a"][0], "src/lib/a", "should inherit Paths.Rel from extended")
		assertEqual(t, extender.Extends, "base.tsconfig.json", "should not fail extending")
	})

	t.Run("parse a tsconfig extending other in parent dir", func(t *testing.T) {
		extender, err := parseTsConfigJSONFile(make(map[string]*TsConfig), identityResolver, ".", "tests/subdir/extends-base.json")
		if err != nil {
			t.Errorf("parseTsConfigJSONFile: %v", err)
		}

		if !extender.ImportHelpers {
			t.Errorf("should inherit compilerOptions.importHelpers")
		}
		assertEqual(t, extender.Paths.Rel, "../src", "should inherit Paths.Rel from extended")
		assertEqual(t, (*extender.Paths.Map)["alias-a"][0], "src/lib/a", "should inherit Paths.Rel from extended")
		assertEqual(t, extender.Extends, "../base.tsconfig.json", "should not fail extending")
	})

	t.Run("parse a tsconfig extending other in parent dir and overriding paths", func(t *testing.T) {
		extender, err := parseTsConfigJSONFile(make(map[string]*TsConfig), identityResolver, ".", "tests/subdir/extends-base-paths.json")
		if err != nil {
			t.Errorf("parseTsConfigJSONFile: %v", err)
		}

		if !extender.ImportHelpers {
			t.Errorf("should inherit compilerOptions.importHelpers")
		}
		assertEqual(t, extender.Paths.Rel, ".", "should override Paths.Rel with local")
		_, aliasAExists := (*extender.Paths.Map)["alias-a"]
		assertTrue(t, !aliasAExists, "should override Paths from extended")
		assertEqual(t, (*extender.Paths.Map)["alias-b"][0], "src/lib/b", "should override Paths.Map")
		assertEqual(t, extender.Extends, "../base.tsconfig.json", "should not fail extending")
	})

	t.Run("parse a tsconfig file extending itself", func(t *testing.T) {
		recursive, err := parseTsConfigJSONFile(make(map[string]*TsConfig), identityResolver, ".", "tests/extends-recursive.json")
		if err != nil {
			t.Errorf("parseTsConfigJSONFile: %v", err)
		}

		assertEqual(t, recursive.Extends, "extends-recursive.json", "should not fail extending itself")
	})

	t.Run("parse a tsconfig file extending an unknown file", func(t *testing.T) {
		notFound, err := parseTsConfigJSONFile(make(map[string]*TsConfig), identityResolver, ".", "tests/extends-404.json")
		if err != nil {
			t.Errorf("parseTsConfigJSONFile: %v", err)
		}

		assertEqual(t, notFound.Extends, "does-not-exist.json", "should not fail extending unknown")
	})

	t.Run("parse a tsconfig file extending a blank string", func(t *testing.T) {
		extendsBlank, err := parseTsConfigJSONFile(make(map[string]*TsConfig), identityResolver, ".", "tests/extends-empty.json")
		if err != nil {
			t.Errorf("parseTsConfigJSONFile: %v", err)
		}

		assertEqual(t, extendsBlank.Extends, "", "should not fail extending an empty str")
	})

	t.Run("parse example tsconfig file with comments, trialing commas", func(t *testing.T) {
		// See https://github.com/msolo/jsonr/issues/1#event-10736439202
		unknown, err := parseTsConfigJSONFile(make(map[string]*TsConfig), identityResolver, ".", "tests/sourcegraph-svelt.json")
		if err != nil {
			t.Errorf("parseTsConfigJSONFile: %v", err)
		}

		assertEqual(t, unknown.Extends, ".svelte-kit/tsconfig.json", "should set Extends to blank when not found")
	})

	t.Run("parse a tsconfig file extending a named-import", func(t *testing.T) {
		extender, err := parseTsConfigJSONFile(make(map[string]*TsConfig), fooResolver, ".", "tests/extends-foo.json")
		if err != nil {
			t.Errorf("parseTsConfigJSONFile: %v", err)
		}

		assertEqual(t, extender.Paths.Rel, "src", "should inherit Paths.Rel from extended")
		assertEqual(t, (*extender.Paths.Map)["alias-a"][0], "src/lib/a", "should inherit Paths.Rel from extended")
		assertEqual(t, extender.Extends, "foo", "should not fail extending")
	})
}

func TestTsconfigParse(t *testing.T) {
	t.Run("parse a tsconfig with empty config", func(t *testing.T) {
		options := parseTest(t, ".", "{}")

		if options.RootDir != "." {
			t.Errorf("ParseTsConfigOptions: RootDir\nactual:   %s\nexpected:  %s\n", options.RootDir, ".")
		}
	})

	t.Run("parse a tsconfig with empty compilerOptions", func(t *testing.T) {
		options := parseTest(t, ".", `{"compilerOptions": {}}`)

		if options.RootDir != "." {
			t.Errorf("ParseTsConfigOptions: RootDir\nactual:   %s\nexpected:  %s\n", options.RootDir, ".")
		}
	})

	t.Run("parse a tsconfig with rootDir ''", func(t *testing.T) {
		options := parseTest(t, ".", `{"compilerOptions": {"rootDir": ""}}`)

		if options.RootDir != "." {
			t.Errorf("ParseTsConfigOptions: RootDir\nactual:   %s\nexpected:  %s\n", options.RootDir, ".")
		}

		out1 := options.ToOutDir("src/foo.ts")
		out2 := options.ToDeclarationOutDir("src/foo.ts")
		out3 := options.ToOutDir("src")
		if out1 != "src/foo.ts" || out2 != "src/foo.ts" || out3 != "src" {
			t.Errorf("Failed to compute rootDir output path: %s", out1)
		}
	})

	t.Run("parse a tsconfig with rootDir '.'", func(t *testing.T) {
		options := parseTest(t, ".", `{"compilerOptions": {"rootDir": "."}}`)

		if options.RootDir != "." {
			t.Errorf("ParseTsConfigOptions: RootDir\nactual:   %s\nexpected:  %s\n", options.RootDir, ".")
		}

		out1 := options.ToOutDir("src/foo.ts")
		out2 := options.ToDeclarationOutDir("src/foo.ts")
		out3 := options.ToOutDir("src")
		if out1 != "src/foo.ts" || out2 != "src/foo.ts" || out3 != "src" {
			t.Errorf("Failed to compute rootDir output path: %s", out1)
		}
	})

	t.Run("parse a tsconfig with rootDir './'", func(t *testing.T) {
		options := parseTest(t, ".", `{"compilerOptions": {"rootDir": "./"}}`)

		if options.RootDir != "." {
			t.Errorf("ParseTsConfigOptions: RootDir\nactual:   %s\nexpected:  %s\n", options.RootDir, ".")
		}

		out1 := options.ToOutDir("src/foo.ts")
		out2 := options.ToDeclarationOutDir("src/foo.ts")
		out3 := options.ToOutDir("src")
		if out1 != "src/foo.ts" || out2 != "src/foo.ts" || out3 != "src" {
			t.Errorf("Failed to compute rootDir output path: %s", out1)
		}
	})

	t.Run("parse a tsconfig with rootDir", func(t *testing.T) {
		options := parseTest(t, ".", `{"compilerOptions": {"rootDir": "src"}}`)

		if options.RootDir != "src" {
			t.Errorf("ParseTsConfigOptions: RootDir\nactual:   %s\nexpected:  %s\n", options.RootDir, "src")
		}

		out1 := options.ToOutDir("src/foo.ts")
		out2 := options.ToDeclarationOutDir("src/foo.ts")
		out3 := options.ToOutDir("src")
		if out1 != "foo.ts" || out2 != "foo.ts" || out3 != "src" {
			t.Errorf("Failed to compute rootDir output path: %s", out1)
		}
	})

	t.Run("parse a tsconfig with rootDir/", func(t *testing.T) {
		options := parseTest(t, ".", `{"compilerOptions": {"rootDir": "src/"}}`)

		if options.RootDir != "src" {
			t.Errorf("ParseTsConfigOptions: RootDir\nactual:   %s\nexpected:  %s\n", options.RootDir, "src")
		}

		out1 := options.ToOutDir("src/foo.ts")
		out2 := options.ToDeclarationOutDir("src/foo.ts")
		out3 := options.ToOutDir("src")
		if out1 != "foo.ts" || out2 != "foo.ts" || out3 != "src" {
			t.Errorf("Failed to compute rootDir output path: %s", out1)
		}
	})

	t.Run("parse a tsconfig with ./rootDir relative", func(t *testing.T) {
		options := parseTest(t, ".", `{"compilerOptions": {"rootDir": "./src"}}`)

		if options.RootDir != "src" {
			t.Errorf("ParseTsConfigOptions: RootDir\nactual:   %s\nexpected:  %s\n", options.RootDir, "src")
		}

		out1 := options.ToOutDir("src/foo.ts")
		out2 := options.ToDeclarationOutDir("src/foo.ts")
		out3 := options.ToOutDir("src")
		if out1 != "foo.ts" || out2 != "foo.ts" || out3 != "src" {
			t.Errorf("Failed to compute rootDir output path: %s", out1)
		}
	})

	t.Run("parse a tsconfig with ./rootDir/ relative", func(t *testing.T) {
		options := parseTest(t, ".", `{"compilerOptions": {"rootDir": "./src/"}}`)

		if options.RootDir != "src" {
			t.Errorf("ParseTsConfigOptions: RootDir\nactual:   %s\nexpected:  %s\n", options.RootDir, "src")
		}

		out1 := options.ToOutDir("src/foo.ts")
		out2 := options.ToDeclarationOutDir("src/foo.ts")
		out3 := options.ToOutDir("src")
		if out1 != "foo.ts" || out2 != "foo.ts" || out3 != "src" {
			t.Errorf("Failed to compute rootDir output path: %s", out1)
		}
	})

	t.Run("parse a tsconfig with rootDir relative extra dots", func(t *testing.T) {
		options := parseTest(t, ".", `{"compilerOptions": {"rootDir": "./src/./foo/../"}}`)

		if options.RootDir != "src" {
			t.Errorf("ParseTsConfigOptions: RootDir\nactual:   %s\nexpected:  %s\n", options.RootDir, "src")
		}
	})

	t.Run("parse tsconfig files with relaxed json", func(t *testing.T) {
		parseTest(t, ".", `{}`)
		parseTest(t, ".", `{"compilerOptions": {}}`)
		parseTest(t, ".", `
			{
				"compilerOptions": {
					"rootDir": "src",
					"baseUrl": ".",
				},
			}
		`)
		parseTest(t, ".", `
			{
				"compilerOptions": {
					// line comment
					"paths": {
						"x": ["./y.ts"], // trailing with comments
					},
					"baseUrl": ".",
				},
			}
		`)
	})

	t.Run("tsconfig paths inheritance", func(t *testing.T) {

		// Mock a config manually to set a custom Rel path (like an external tsconfig was loaded)
		config := &TsConfig{
			ConfigDir: "tsconfig_test",
			Paths: &TsConfigPaths{
				Rel: "../libs/ts/liba",
				Map: &map[string][]string{
					"@org/liba/*": {"src/*"},
				},
			},
		}

		assertExpand(t, config, "@org/liba/test", "libs/ts/liba/src/test", "tsconfig_test/@org/liba/test")
	})

	t.Run("tsconfig paths expansion basic", func(t *testing.T) {
		// Initial request: https://github.com/aspect-build/aspect-cli/issues/396
		config := parseTest(t, "tsconfig_test", `{
			"compilerOptions": {
			  "declaration": true,
			  "baseUrl": ".",
			  "paths": {
				"@org/*": [
				  "b/src/*"
				]
			  }
			}
		  }`)

		assertExpand(t, config, "@org/lib", "tsconfig_test/b/src/lib", "tsconfig_test/@org/lib")
	})

	t.Run("tsconfig paths jquery example", func(t *testing.T) {
		// jQuery examples from
		// https://www.typescriptlang.org/docs/handbook/module-resolution.html#path-mapping

		config1 := parseTest(t, ".", `{
			"compilerOptions": {
			  "baseUrl": ".",
			  "paths": {
				"jquery": ["node_modules/jquery/dist/jquery"]
			  }
			}
		  }`)
		assertExpand(t, config1, "jquery", "node_modules/jquery/dist/jquery", "jquery")

		config2 := parseTest(t, ".", `{
			"compilerOptions": {
			  "baseUrl": "src",
			  "paths": {
				"jquery": ["../node_modules/jquery/dist/jquery"]
			  }
			}
		  }`)
		assertExpand(t, config2, "jquery", "node_modules/jquery/dist/jquery", "src/jquery")
	})

	t.Run("tsconfig paths generated example", func(t *testing.T) {
		// 'generated' example from
		// https://www.typescriptlang.org/docs/handbook/module-resolution.html#path-mapping

		config1 := parseTest(t, ".", `{
			"compilerOptions": {
			  "baseUrl": ".",
			  "paths": {
				"*": ["*", "generated/*"]
			  }
			}
		  }`)
		assertExpand(t, config1, "x", "x", "generated/x", "x")

		config2 := parseTest(t, ".", `{
			"compilerOptions": {
			  "baseUrl": "foo",
			  "paths": {
				"*": ["*", "../generated/*"]
			  }
			}
		  }`)
		assertExpand(t, config2, "x", "foo/x", "generated/x", "foo/x")
	})

	t.Run("tsconfig paths expansion", func(t *testing.T) {
		config := parseTest(t, "tsconfig_test", `{
				"compilerOptions": {
					"baseUrl": ".",
					"paths": {
						"test0": ["./test0-success.ts"],
						"test1/*": ["./test1-success.ts"],
						"test2/*": ["./test2-success/*"],
						"t*t3/foo": ["./test3-succ*s.ts"],
						"test4/*": ["./test4-first/*", "./test4-second/*"],
						"test5/*": ["./test5-first/*", "./test5-second/*"]
					}
				}
			}`)

		assertExpand(t, config, "test0", "tsconfig_test/test0-success.ts", "tsconfig_test/test0")
		assertExpand(t, config, "test1/bar", "tsconfig_test/test1-success.ts", "tsconfig_test/test1/bar")
		assertExpand(t, config, "test1/foo", "tsconfig_test/test1-success.ts", "tsconfig_test/test1/foo")
		assertExpand(t, config, "test2/foo", "tsconfig_test/test2-success/foo", "tsconfig_test/test2/foo")
		assertExpand(t, config, "test3/x", "tsconfig_test/test3/x")

		assertExpand(t, config, "tXt3/foo", "tsconfig_test/test3-succXs.ts", "tsconfig_test/tXt3/foo")
		assertExpand(t, config, "t123t3/foo", "tsconfig_test/test3-succ123s.ts", "tsconfig_test/t123t3/foo")
		assertExpand(t, config, "t-t3/foo", "tsconfig_test/test3-succ-s.ts", "tsconfig_test/t-t3/foo")

		assertExpand(t, config, "test4/x", "tsconfig_test/test4-first/x", "tsconfig_test/test4-second/x", "tsconfig_test/test4/x")
	})

	t.Run("tsconfig paths expansion star-length tie-breaker", func(t *testing.T) {
		config := parseTest(t, "tsconfig_test", `{
				"compilerOptions": {
					"baseUrl": ".",
					"paths": {
						"lib/*": ["fallback/*"],
						"lib/a": ["a-direct"],
						"l*": ["l-star/*"],
						"lib*": ["lib-star/*"],
						"li*": ["li-star/*"],
						"lib*-suff": ["lib-star-suff/*"]
					}
				}
			}`)

		assertExpand(t, config, "lib/a", "tsconfig_test/a-direct", "tsconfig_test/fallback/a", "tsconfig_test/lib-star/a", "tsconfig_test/li-star/b/a", "tsconfig_test/l-star/ib/a", "tsconfig_test/lib/a")
	})

	t.Run("tsconfig * paths", func(t *testing.T) {
		config := parseTest(t, ".", `{
			"compilerOptions": {
			  "baseUrl": ".",
			  "paths": {
				"*": ["sub/*", "*"]
			  }
			}
		  }`)

		assertExpand(t, config, "a", "sub/a", "a", "a")
	})

	t.Run("tsconfig importHelpers", func(t *testing.T) {
		if parseTest(t, ".", "{}").ImportHelpers {
			t.Errorf("ImportHelpers should be false by default")
		}

		config := parseTest(t, ".", `{
			"compilerOptions": {
			  "importHelpers": true
			}
		  }`)

		if !config.ImportHelpers {
			t.Errorf("ParseTsConfigOptions: ImportHelpers\nactual:   %v\nexpected: %v\n", config.ImportHelpers, true)
		}
	})
}

func TestTsconfigOutRootDirs(t *testing.T) {
	t.Run("empty config", func(t *testing.T) {
		o1 := parseTest(t, ".", `{}`)
		assertEqual(t, o1.ToOutDir("foo.ts"), "foo.ts", "empty config")
		assertEqual(t, o1.ToDeclarationOutDir("foo.ts"), "foo.ts", "empty config")

		o2 := parseTest(t, ".", `{"compilerOptions": {"rootDir": "./"}}`)
		assertEqual(t, o2.ToOutDir("foo.ts"), "foo.ts", "empty rel config")
		assertEqual(t, o2.ToDeclarationOutDir("foo.ts"), "foo.ts", "empty rel config")
	})

	t.Run("rootDir config", func(t *testing.T) {
		o1 := parseTest(t, ".", `{"compilerOptions": {"rootDir": "./src"}}`)
		assertEqual(t, o1.ToOutDir("src/foo.ts"), "foo.ts", "empty config")
		assertEqual(t, o1.ToDeclarationOutDir("src/foo.ts"), "foo.ts", "empty config")

		o2 := parseTest(t, ".", `{"compilerOptions": {"rootDir": "src"}}`)
		assertEqual(t, o2.ToOutDir("src/foo.ts"), "foo.ts", "empty config")
		assertEqual(t, o2.ToDeclarationOutDir("src/foo.ts"), "foo.ts", "empty config")

		o3 := parseTest(t, ".", `{"compilerOptions": {"rootDir": "src/foo/.."}}`)
		assertEqual(t, o3.ToOutDir("src/foo.ts"), "foo.ts", "empty config")
		assertEqual(t, o3.ToDeclarationOutDir("src/foo.ts"), "foo.ts", "empty config")
	})

	t.Run("outDir config", func(t *testing.T) {
		o1 := parseTest(t, ".", `{"compilerOptions": {"outDir": "dist"}}`)
		assertEqual(t, o1.ToOutDir("foo.ts"), "dist/foo.ts", "empty config")
		assertEqual(t, o1.ToDeclarationOutDir("foo.ts"), "dist/foo.ts", "empty config")

		o2 := parseTest(t, ".", `{"compilerOptions": {"outDir": "./dist"}}`)
		assertEqual(t, o2.ToOutDir("foo.ts"), "dist/foo.ts", "empty config")
		assertEqual(t, o2.ToDeclarationOutDir("foo.ts"), "dist/foo.ts", "empty config")

		o3 := parseTest(t, ".", `{"compilerOptions": {"outDir": "./dist/"}}`)
		assertEqual(t, o3.ToOutDir("foo.ts"), "dist/foo.ts", "empty config")
		assertEqual(t, o3.ToDeclarationOutDir("foo.ts"), "dist/foo.ts", "empty config")

		o4 := parseTest(t, ".", `{"compilerOptions": {"outDir": "./dist/foo/.."}}`)
		assertEqual(t, o4.ToOutDir("foo.ts"), "dist/foo.ts", "empty config")
		assertEqual(t, o4.ToDeclarationOutDir("foo.ts"), "dist/foo.ts", "empty config")
	})

	t.Run("rootDir + outDir config", func(t *testing.T) {
		o1 := parseTest(t, ".", `{"compilerOptions": {"rootDir": "./src", "outDir": "dist"}}`)
		assertEqual(t, o1.ToOutDir("src/foo.ts"), "dist/foo.ts", "in rootdir")
		assertEqual(t, o1.ToDeclarationOutDir("src/foo.ts"), "dist/foo.ts", "in rootdir")
		assertEqual(t, o1.ToOutDir("src.ts"), "dist/src.ts", "not in rootdir")
		assertEqual(t, o1.ToDeclarationOutDir("src.ts"), "dist/src.ts", "not in rootdir")
		assertEqual(t, o1.ToOutDir("src-other/src.ts"), "dist/src-other/src.ts", "has similar rootdir")
		assertEqual(t, o1.ToDeclarationOutDir("src-other/src.ts"), "dist/src-other/src.ts", "has similar rootdir")
		assertEqual(t, o1.ToOutDir("src"), "dist/src", "invalid rootdir prefix")
		assertEqual(t, o1.ToDeclarationOutDir("src"), "dist/src", "invalid rootdir prefix")
	})

	t.Run("rootDir + declarationDir config", func(t *testing.T) {
		o1 := parseTest(t, ".", `{"compilerOptions": {"rootDir": "./src", "declarationDir": "dist"}}`)
		assertEqual(t, o1.ToDeclarationOutDir("src/foo.ts"), "dist/foo.ts", "in rootdir")
		assertEqual(t, o1.ToDeclarationOutDir("src.ts"), "dist/src.ts", "not in rootdir")
		assertEqual(t, o1.ToDeclarationOutDir("src-other/src.ts"), "dist/src-other/src.ts", "has similar rootdir")
		assertEqual(t, o1.ToDeclarationOutDir("src"), "dist/src", "invalid rootdir prefix")
	})

	t.Run("rootDir + outDir + declarationDir config", func(t *testing.T) {
		o1 := parseTest(t, ".", `{"compilerOptions": {"rootDir": "./src", "outDir": "notMe", "declarationDir": "dist"}}`)
		assertEqual(t, o1.ToDeclarationOutDir("src/foo.ts"), "dist/foo.ts", "in rootdir")
		assertEqual(t, o1.ToDeclarationOutDir("src.ts"), "dist/src.ts", "not in rootdir")
		assertEqual(t, o1.ToDeclarationOutDir("src-other/src.ts"), "dist/src-other/src.ts", "has similar rootdir")
		assertEqual(t, o1.ToDeclarationOutDir("src"), "dist/src", "invalid rootdir prefix")
	})

	t.Run("rootDir + outDir + declarationDir-root config", func(t *testing.T) {
		o1 := parseTest(t, ".", `{"compilerOptions": {"rootDir": "./src", "outDir": "notMe", "declarationDir": "."}}`)
		assertEqual(t, o1.ToDeclarationOutDir("src/foo.ts"), "foo.ts", "in rootdir")
		assertEqual(t, o1.ToDeclarationOutDir("src.ts"), "src.ts", "not in rootdir")
		assertEqual(t, o1.ToDeclarationOutDir("src-other/src.ts"), "src-other/src.ts", "has similar rootdir")
		assertEqual(t, o1.ToDeclarationOutDir("src"), "src", "invalid rootdir prefix")
	})
}
