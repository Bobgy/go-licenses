// Copyright 2021 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	configmodule "github.com/google/go-licenses/v2/config"
	"github.com/google/go-licenses/v2/ghutils"
	"github.com/google/go-licenses/v2/gocli"
	"github.com/google/go-licenses/v2/goutils"
	"github.com/google/go-licenses/v2/licenses"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

// csvCmd represents the csv command
var csvCmd = &cobra.Command{
	Use:   "csv",
	Short: "Generate dependency license csv",
	Long: `Generate license_info.csv for go modules. It mainly uses GitHub
	license API to get license info. There may be false positives. Use it at
	your own risk.`,
	Run: func(cmd *cobra.Command, args []string) {
		err := csvImp()
		if err != nil {
			klog.Exit(err)
		}
	},
}

func init() {
	rootCmd.AddCommand(csvCmd)

	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// csvCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// csvCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}

func csvImp() (err error) {
	config, err := configmodule.Load("")
	if err != nil {
		return err
	}

	if config.Module.LicenseDB.Path == "" {
		config.Module.LicenseDB.Path, err = defaultLicenseDB()
		if err != nil {
			klog.Exit(fmt.Errorf("licenseDB.path is empty, also failed to get defaulut licenseDB path: %w", err))
		}
		klog.V(2).InfoS("Config: use default license DB")
	}
	klog.V(2).InfoS("Config: license DB path", "path", config.Module.LicenseDB.Path)

	goModules, err := gocli.ListModulesInBinary(config.Module.Go.Binary.Path)
	if err != nil {
		return err
	}
	mainModuleAbsPath, err := filepath.Abs(config.Module.Go.Path)
	if err != nil {
		return err
	}
	mainModule := []gocli.Module{{
		Path:    config.Module.Go.Module,
		Dir:     mainModuleAbsPath,
		Version: config.Module.Go.Version,
		Main:    true,
	}}
	goModules = append(mainModule, goModules...)
	klog.InfoS("Done: found dependencies", "count", len(goModules))
	if klog.V(3).Enabled() {
		for _, goModule := range goModules {
			klog.InfoS("dependency", "module", goModule.Path, "version", goModule.Version, "Dir", goModule.Dir)
		}
	}
	f, err := os.Create(config.Module.Csv.Path)
	if err != nil {
		return errors.Wrapf(err, "Creating license csv file")
	}
	defer func() {
		closeErr := f.Close()
		if err == nil {
			// When there are no other errors, surface close file error.
			// Otherwise file content may not be flushed to disk successfully.
			err = closeErr
		}
	}()
	_, err = f.WriteString("# Generated by https://github.com/google/go-licenses/v2. DO NOT EDIT.\n")
	if err != nil {
		return err
	}
	licenseCount := 0
	errorCount := 0
	for _, goModule := range goModules {
		report := func(err error, args ...interface{}) {
			errorCount = errorCount + 1
			errorArgs := []interface{}{"module", goModule.Path}
			errorArgs = append(errorArgs, args...)
			klog.ErrorS(err, "Failed", errorArgs...)
		}
		var override configmodule.ModuleOverride
		for _, o := range config.Module.Overrides {
			if o.Name == goModule.Path {
				override = o
			}
		}
		// When override.Version == "", the override apply to any version.
		if override.Version != "" && override.Version != goModule.Version {
			report(fmt.Errorf("override version mismatch: version %q!=%q", goModule.Version, override.Version))
			continue
		}
		if override.Skip {
			klog.InfoS("Skipped", "module", goModule.Path)
			continue
		}
		repo, errGetGithubRepo := goutils.GetGithubRepo(goModule.Path)
		// this is not immediately an error, because we might specify override.License.Url below
		type licenseInfo struct {
			spdxId        string // required
			licensePath   string // optional, required when url is not supplied
			url           string // optional
			subModulePath string // optional
			lineStart     int    // optional
			lineEnd       int    // optional
		}
		hasReportedGetGithubRepoErr := false
		writeLicenseInfo := func(info licenseInfo) error {
			if len(info.spdxId) == 0 {
				return fmt.Errorf("failed writeLicenseInfo: info.spdxId required")
			}
			url := info.url
			if len(url) == 0 {
				if len(info.licensePath) == 0 {
					return fmt.Errorf("failed writeLicenseInfo: info.licensePath required when info.url is empty")
				}
				if repo == nil && !hasReportedGetGithubRepoErr {
					// now we need to use repo, so this becomes a fatal error
					report(errGetGithubRepo)
					hasReportedGetGithubRepoErr = true // only report once
					// when repo == nil, repo.RemoteUrl has fallback behavior to use local path,
					// so keep running to show more information to debug.
				}
				licensePath := info.licensePath
				if info.subModulePath != "" {
					licensePath = info.subModulePath + "/" + info.licensePath
				}
				url, err = repo.RemoteUrl(ghutils.RemoteUrlArgs{
					Path:      licensePath,
					Version:   goModule.Version,
					LineStart: info.lineStart,
					LineEnd:   info.lineEnd,
				})
				if err != nil {
					return err
				}
			}
			moduleString := goModule.Path
			if info.subModulePath != "" {
				moduleString = moduleString + "/" + info.subModulePath
			}
			_, err := f.WriteString(fmt.Sprintf(
				"%s, %s, %s\n",
				moduleString,
				url,
				info.spdxId))
			if err != nil {
				return fmt.Errorf("Failed to write string: %w", err)
			}
			licenseCount = licenseCount + 1
			return nil
		}

		if len(override.License.Path) > 0 {
			license := override.License
			if len(license.SpdxId) == 0 {
				report(fmt.Errorf("override.license.spdxId required"))
				continue
			}
			klog.V(4).InfoS("License overridden", "module", goModule.Path, "version", goModule.Version, "Dir", goModule.Dir)
			klog.V(5).InfoS("Override config", "override", fmt.Sprintf("%+v", override))
			err := writeLicenseInfo(licenseInfo{
				url:         license.Url,
				licensePath: license.Path,
				spdxId:      license.SpdxId,
				lineStart:   license.LineStart,
				lineEnd:     license.LineEnd,
			})
			if err != nil {
				return err
			}
			for _, subModule := range override.SubModules {
				license := subModule.License
				if len(subModule.Path) == 0 || len(license.Path) == 0 || len(license.SpdxId) == 0 {
					report(fmt.Errorf("override.subModule: path, license.path and license.spdxId are required: subModule=%+v", subModule))
					continue
				}
				err := writeLicenseInfo(licenseInfo{
					url:           license.Url,
					licensePath:   license.Path,
					spdxId:        license.SpdxId,
					lineStart:     license.LineStart,
					lineEnd:       license.LineEnd,
					subModulePath: subModule.Path,
				})
				if err != nil {
					return err
				}
			}
			continue
		}

		klog.V(4).InfoS("Scanning", "module", goModule.Path, "version", goModule.Version, "Dir", goModule.Dir)
		licensesFound, err := licenses.ScanDir(goModule.Dir, licenses.ScanDirOptions{ExcludePaths: override.ExcludePaths, DbPath: config.Module.LicenseDB.Path})
		if err != nil {
			report(err)
			continue
		}
		if len(licensesFound) == 0 {
			report(errors.Errorf("licenses not found"))
			continue
		}

		for _, license := range licensesFound {
			klog.V(3).InfoS("License", "module", goModule.Path, "SpdxId", license.SpdxId, "path", filepath.Join(goModule.Dir, license.Path))
			writeLicenseInfo(licenseInfo{
				spdxId:      license.SpdxId,
				licensePath: license.Path,
			})
			if err != nil {
				return err
			}
		}
	}
	if errorCount > 0 {
		return fmt.Errorf("Failed to scan licenses for %v module(s)", errorCount)
	}
	klog.InfoS("Done: scan licenses of dependencies", "licenseCount", licenseCount, "moduleCount", len(goModules))
	return nil
}

func defaultLicenseDB() (string, error) {
	execDir, err := findExecutable()
	if err != nil {
		return "", fmt.Errorf("findLicenseDB failed: %w", err)
	}
	return filepath.Join(execDir, "licenses"), nil
}

func findExecutable() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("findExecutable failed: %w", err)
	}
	dirPath := filepath.Dir(path)
	return dirPath, nil
}