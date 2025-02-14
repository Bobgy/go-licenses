// Copyright 2019 Google Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package licenses

import (
	"context"
	"fmt"
	"go/build"
	"io/ioutil"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/golang/glog"
	"github.com/Bobgy/go-licenses/v2/internal/third_party/pkgsite/source"
	"golang.org/x/tools/go/packages"
)

// Library is a collection of packages covered by the same license file.
type Library struct {
	// LicensePath is the path of the file containing the library's license.
	LicensePath string
	// Packages contains import paths for Go packages in this library.
	// It may not be the complete set of all packages in the library.
	Packages []string
	// Parent go module.
	module *Module
}

// PackagesError aggregates all Packages[].Errors into a single error.
type PackagesError struct {
	pkgs []*packages.Package
}

func (e PackagesError) Error() string {
	var str strings.Builder
	str.WriteString(fmt.Sprintf("errors for %q:", e.pkgs))
	packages.Visit(e.pkgs, nil, func(pkg *packages.Package) {
		for _, err := range pkg.Errors {
			str.WriteString(fmt.Sprintf("\n%s: %s", pkg.PkgPath, err))
		}
	})
	return str.String()
}

// Libraries returns the collection of libraries used by this package, directly or transitively.
// A library is a collection of one or more packages covered by the same license file.
// Packages not covered by a license will be returned as individual libraries.
// Standard library packages will be ignored.
func Libraries(ctx context.Context, classifier Classifier, importPaths ...string) ([]*Library, error) {
	cfg := &packages.Config{
		Context: ctx,
		Mode:    packages.NeedImports | packages.NeedDeps | packages.NeedFiles | packages.NeedName | packages.NeedModule,
	}

	rootPkgs, err := packages.Load(cfg, importPaths...)
	if err != nil {
		return nil, err
	}

	pkgs := map[string]*packages.Package{}
	pkgsByLicense := make(map[string][]*packages.Package)
	errorOccurred := false
	packages.Visit(rootPkgs, func(p *packages.Package) bool {
		if len(p.Errors) > 0 {
			errorOccurred = true
			return false
		}
		if isStdLib(p) {
			// No license requirements for the Go standard library.
			return false
		}
		if len(p.OtherFiles) > 0 {
			glog.Warningf("%q contains non-Go code that can't be inspected for further dependencies:\n%s", p.PkgPath, strings.Join(p.OtherFiles, "\n"))
		}
		var pkgDir string
		switch {
		case len(p.GoFiles) > 0:
			pkgDir = filepath.Dir(p.GoFiles[0])
		case len(p.CompiledGoFiles) > 0:
			pkgDir = filepath.Dir(p.CompiledGoFiles[0])
		case len(p.OtherFiles) > 0:
			pkgDir = filepath.Dir(p.OtherFiles[0])
		default:
			// This package is empty - nothing to do.
			return true
		}
		licensePath, err := Find(pkgDir, p.Module.Dir, classifier)
		if err != nil {
			glog.Errorf("Failed to find license for %s: %v", p.PkgPath, err)
		}
		pkgs[p.PkgPath] = p
		pkgsByLicense[licensePath] = append(pkgsByLicense[licensePath], p)
		return true
	}, nil)
	if errorOccurred {
		return nil, PackagesError{
			pkgs: rootPkgs,
		}
	}

	var libraries []*Library
	for licensePath, pkgs := range pkgsByLicense {
		if licensePath == "" {
			// No license for these packages - return each one as a separate library.
			for _, p := range pkgs {
				libraries = append(libraries, &Library{
					Packages: []string{p.PkgPath},
					module:   newModule(p.Module),
				})
			}
			continue
		}
		lib := &Library{
			LicensePath: licensePath,
		}
		for _, pkg := range pkgs {
			lib.Packages = append(lib.Packages, pkg.PkgPath)
			if lib.module == nil {
				// All the sub packages should belong to the same module.
				lib.module = newModule(pkg.Module)
			}
			if lib.module != nil && lib.module.Path != "" && lib.module.Dir == "" {
				// A known cause is that the module is vendored, so some information is lost.
				splits := strings.SplitN(lib.LicensePath, "/vendor/", 2)
				if len(splits) != 2 {
					glog.Warningf("module %s does not have dir and it's not vendored, cannot discover the license URL. Report to go-licenses developer if you see this.", lib.module.Path)
				} else {
					// This is vendored. Handle this known special case.
					parentModDir := splits[0]
					var parentPkg *packages.Package
					for _, rootPkg := range rootPkgs {
						if rootPkg.Module != nil && rootPkg.Module.Dir == parentModDir {
							parentPkg = rootPkg
							break
						}
					}
					if parentPkg == nil {
						glog.Warningf("cannot find parent package of vendored module %s", lib.module.Path)
					} else {
						// Vendored modules should be commited in the parent module, so it counts as part of the
						// parent module.
						lib.module = newModule(parentPkg.Module)
					}
				}
			}
		}
		libraries = append(libraries, lib)
	}
	// Sort libraries to produce a stable result for snapshot diffing.
	sort.Slice(libraries, func(i, j int) bool {
		return libraries[i].Name() < libraries[j].Name()
	})
	return libraries, nil
}

// Name is the common prefix of the import paths for all of the packages in this library.
func (l *Library) Name() string {
	return commonAncestor(l.Packages)
}

func commonAncestor(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	if len(paths) == 1 {
		return paths[0]
	}
	sort.Strings(paths)
	min, max := paths[0], paths[len(paths)-1]
	lastSlashIndex := 0
	for i := 0; i < len(min) && i < len(max); i++ {
		if min[i] != max[i] {
			return min[:lastSlashIndex]
		}
		if min[i] == '/' {
			lastSlashIndex = i
		}
	}
	return min
}

func (l *Library) String() string {
	return l.Name()
}

// testOnlySkipValidation is an internal flag to skip validation during testing,
// because we cannot easily set up actual license files on disk.
var testOnlySkipValidation = false

// LicenseURL attempts to determine the URL for the license file in this library
// using go module name and version.
func (l *Library) LicenseURL(ctx context.Context) (string, error) {
	if l == nil {
		return "", fmt.Errorf("library is nil")
	}
	filePath := l.LicensePath
	wrap := func(err error) error {
		return fmt.Errorf("getting file URL in library %s: %w", l.Name(), err)
	}
	m := l.module
	if m == nil {
		return "", wrap(fmt.Errorf("empty go module info"))
	}
	if m.Dir == "" {
		return "", wrap(fmt.Errorf("empty go module dir"))
	}
	client := source.NewClient(time.Second * 20)
	remote, err := source.ModuleInfo(ctx, client, m.Path, m.Version)
	if err != nil {
		return "", wrap(err)
	}
	if m.Version == "" {
		// This always happens for the module in development.
		// Note#1 if we pass version=HEAD to source.ModuleInfo, github tag for modules not at the root
		// of the repo will be incorrect, because there's a convention that:
		// * I have a module at github.com/Bobgy/go-licenses/v2/submod.
		// * The module is of version v1.0.0.
		// Then the github tag should be submod/v1.0.0.
		// In our case, if we pass HEAD as version, the result commit will be submod/HEAD which is incorrect.
		// Therefore, to workaround this problem, we directly set the commit after getting module info.
		//
		// Note#2 repos have different branches as default, some use the
		// master branch and some use the main branch. However, HEAD
		// always refers to the default branch, so it's better than
		// both of master/main when we do not know which branch is default.
		// Examples:
		// * https://github.com/Bobgy/go-licenses/v2/blob/HEAD/LICENSE
		// points to latest commit of master branch.
		// * https://github.com/google/licenseclassifier/blob/HEAD/LICENSE
		// points to latest commit of main branch.
		remote.SetCommit("HEAD")
		glog.Warningf("module %s has empty version, defaults to HEAD. The license URL may be incorrect. Please verify!", m.Path)
	}
	relativePath, err := filepath.Rel(m.Dir, filePath)
	if err != nil {
		return "", wrap(err)
	}
	url := remote.FileURL(relativePath)
	if testOnlySkipValidation {
		return url, nil
	}
	// An error during validation, the URL may still be valid.
	validationError := func(err error) error {
		return fmt.Errorf("failed to validate %s: %w", url, err)
	}
	localContentBytes, err := ioutil.ReadFile(l.LicensePath)
	if err != nil {
		return "", validationError(err)
	}
	localContent := string(localContentBytes)
	// Attempt 1
	rawURL := remote.RawURL(relativePath)
	if rawURL == "" {
		glog.Warningf(
			"Skipping license URL validation, because %s. Please verify whether %s matches content of %s manually!",
			validationError(fmt.Errorf("remote repo %s does not support raw URL", remote)),
			url,
			l.LicensePath,
		)
		return url, nil
	}
	validationError1 := validate(rawURL, localContent)
	if validationError1 == nil {
		// The found URL is valid!
		return url, nil
	}
	if relativePath != "LICENSE" {
		return "", validationError1
	}
	// Attempt 2 when relativePath == "LICENSE"
	// When module is at a subdir, the LICENSE file we find at root
	// of the module may actually lie in the root of the repo, due
	// to special go module behavior.
	// Reference: https://github.com/Bobgy/go-licenses/v2/issues/73#issuecomment-1005587408.
	url2 := remote.RepoFileURL("LICENSE")
	rawURL2 := remote.RepoRawURL("LICENSE")
	if url2 == url {
		// Return early, because the second attempt resolved to the same file.
		return "", validationError1
	}
	// For the same remote, no need to check rawURL != "" again.
	validationError2 := validate(rawURL2, localContent)
	if validationError2 == nil {
		return url2, nil
	}
	return "", fmt.Errorf("cannot infer remote URL for %s, failed attempts:\n\tattempt 1: %s\n\tattempt 2: %s", l.LicensePath, validationError1, validationError2)
}

// validate validates content of rawURL matches localContent.
func validate(rawURL string, localContent string) error {
	remoteContent, err := download(rawURL)
	if err != nil {
		// Retry after 1 sec.
		time.Sleep(time.Second)
		remoteContent, err = download(rawURL)
		if err != nil {
			return err
		}
	}
	if remoteContent != localContent {
		return fmt.Errorf("local license file content does not match remote license URL %s", rawURL)
	}
	return nil
}

func download(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("download(%q): %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("download(%q): response status code %v not OK", url, resp.StatusCode)
	}
	bodyBytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("download(%q): failed to read from response body: %w", url, err)
	}
	return string(bodyBytes), nil
}

// isStdLib returns true if this package is part of the Go standard library.
func isStdLib(pkg *packages.Package) bool {
	if pkg.Name == "unsafe" {
		// Special case unsafe stdlib, because it does not contain go files.
		return true
	}
	if len(pkg.GoFiles) == 0 {
		return false
	}
	return strings.HasPrefix(pkg.GoFiles[0], build.Default.GOROOT)
}
