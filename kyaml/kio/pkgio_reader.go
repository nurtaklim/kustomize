// Copyright 2019 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package kio

import (
	"fmt"
	"os"
	"path/filepath"

	"sigs.k8s.io/kustomize/kyaml/errors"
	"sigs.k8s.io/kustomize/kyaml/kio/kioutil"
	"sigs.k8s.io/kustomize/kyaml/sets"
	"sigs.k8s.io/kustomize/kyaml/yaml"
)

// requiredResourcePackageAnnotations are annotations that are required to write resources back to
// files.
var requiredResourcePackageAnnotations = []string{kioutil.IndexAnnotation, kioutil.PathAnnotation}

// PackageBuffer implements Reader and Writer, storing Resources in a local field.
type PackageBuffer struct {
	Nodes []*yaml.RNode
}

func (r *PackageBuffer) Read() ([]*yaml.RNode, error) {
	return r.Nodes, nil
}

func (r *PackageBuffer) Write(nodes []*yaml.RNode) error {
	r.Nodes = nodes
	return nil
}

// LocalPackageReadWriter reads and writes Resources from / to a local directory.
// When writing, LocalPackageReadWriter will delete files if all of the Resources from
// that file have been removed from the output.
type LocalPackageReadWriter struct {
	Kind string `yaml:"kind,omitempty"`

	KeepReaderAnnotations bool `yaml:"keepReaderAnnotations,omitempty"`

	// PackagePath is the path to the package directory.
	PackagePath string `yaml:"path,omitempty"`

	// PackageFileName is the name of file containing package metadata.
	// It will be used to identify package.
	PackageFileName string `yaml:"packageFileName,omitempty"`

	// MatchFilesGlob configures Read to only read Resources from files matching any of the
	// provided patterns.
	// Defaults to ["*.yaml", "*.yml"] if empty.  To match all files specify ["*"].
	MatchFilesGlob []string `yaml:"matchFilesGlob,omitempty"`

	// IncludeSubpackages will configure Read to read Resources from subpackages.
	// Subpackages are identified by presence of PackageFileName.
	IncludeSubpackages bool `yaml:"includeSubpackages,omitempty"`

	// ErrorIfNonResources will configure Read to throw an error if yaml missing missing
	// apiVersion or kind is read.
	ErrorIfNonResources bool `yaml:"errorIfNonResources,omitempty"`

	// OmitReaderAnnotations will cause the reader to skip annotating Resources with the file
	// path and mode.
	OmitReaderAnnotations bool `yaml:"omitReaderAnnotations,omitempty"`

	// SetAnnotations are annotations to set on the Resources as they are read.
	SetAnnotations map[string]string `yaml:"setAnnotations,omitempty"`

	// NoDeleteFiles if set to true, LocalPackageReadWriter won't delete any files
	NoDeleteFiles bool `yaml:"noDeleteFiles,omitempty"`

	files sets.String
}

func (r *LocalPackageReadWriter) Read() ([]*yaml.RNode, error) {
	nodes, err := LocalPackageReader{
		PackagePath:         r.PackagePath,
		MatchFilesGlob:      r.MatchFilesGlob,
		IncludeSubpackages:  r.IncludeSubpackages,
		ErrorIfNonResources: r.ErrorIfNonResources,
		SetAnnotations:      r.SetAnnotations,
		PackageFileName:     r.PackageFileName,
	}.Read()
	if err != nil {
		return nil, errors.Wrap(err)
	}
	// keep track of all the files
	if !r.NoDeleteFiles {
		r.files, err = r.getFiles(nodes)
		if err != nil {
			return nil, errors.Wrap(err)
		}
	}
	return nodes, nil
}

func (r *LocalPackageReadWriter) Write(nodes []*yaml.RNode) error {
	newFiles, err := r.getFiles(nodes)
	if err != nil {
		return errors.Wrap(err)
	}
	var clear []string
	for k := range r.SetAnnotations {
		clear = append(clear, k)
	}
	err = LocalPackageWriter{
		PackagePath:           r.PackagePath,
		ClearAnnotations:      clear,
		KeepReaderAnnotations: r.KeepReaderAnnotations,
	}.Write(nodes)
	if err != nil {
		return errors.Wrap(err)
	}
	deleteFiles := r.files.Difference(newFiles)
	for f := range deleteFiles {
		if err = os.Remove(filepath.Join(r.PackagePath, f)); err != nil {
			return errors.Wrap(err)
		}
	}
	return nil
}

func (r *LocalPackageReadWriter) getFiles(nodes []*yaml.RNode) (sets.String, error) {
	val := sets.String{}
	for _, n := range nodes {
		path, _, err := kioutil.GetFileAnnotations(n)
		if err != nil {
			return nil, errors.Wrap(err)
		}
		val.Insert(path)
	}
	return val, nil
}

// LocalPackageReader reads ResourceNodes from a local package.
type LocalPackageReader struct {
	Kind string `yaml:"kind,omitempty"`

	// PackagePath is the path to the package directory.
	PackagePath string `yaml:"path,omitempty"`

	// PackageFileName is the name of file containing package metadata.
	// It will be used to identify package.
	PackageFileName string `yaml:"packageFileName,omitempty"`

	// MatchFilesGlob configures Read to only read Resources from files matching any of the
	// provided patterns.
	// Defaults to ["*.yaml", "*.yml"] if empty.  To match all files specify ["*"].
	MatchFilesGlob []string `yaml:"matchFilesGlob,omitempty"`

	// IncludeSubpackages will configure Read to read Resources from subpackages.
	// Subpackages are identified by presence of PackageFileName.
	IncludeSubpackages bool `yaml:"includeSubpackages,omitempty"`

	// ErrorIfNonResources will configure Read to throw an error if yaml missing missing
	// apiVersion or kind is read.
	ErrorIfNonResources bool `yaml:"errorIfNonResources,omitempty"`

	// OmitReaderAnnotations will cause the reader to skip annotating Resources with the file
	// path and mode.
	OmitReaderAnnotations bool `yaml:"omitReaderAnnotations,omitempty"`

	// SetAnnotations are annotations to set on the Resources as they are read.
	SetAnnotations map[string]string `yaml:"setAnnotations,omitempty"`
}

var _ Reader = LocalPackageReader{}

var DefaultMatch = []string{"*.yaml", "*.yml"}
var JSONMatch = []string{"*.json"}
var MatchAll = append(DefaultMatch, JSONMatch...)

// Read reads the Resources.
func (r LocalPackageReader) Read() ([]*yaml.RNode, error) {
	if r.PackagePath == "" {
		return nil, fmt.Errorf("must specify package path")
	}

	// use slash for path
	r.PackagePath = filepath.ToSlash(r.PackagePath)
	if len(r.MatchFilesGlob) == 0 {
		r.MatchFilesGlob = DefaultMatch
	}

	var operand ResourceNodeSlice
	var pathRelativeTo string
	var err error
	ignoreFilesMatcher := &IgnoreFilesMatcher{}
	r.PackagePath, err = filepath.Abs(r.PackagePath)
	if err != nil {
		return nil, errors.Wrap(err)
	}
	err = filepath.Walk(r.PackagePath, func(
		path string, info os.FileInfo, err error) error {
		if err != nil {
			return errors.Wrap(err)
		}

		// is this the user specified path?
		if path == r.PackagePath {
			if info.IsDir() {
				// skip the root package directory, but check for a
				// .krmignore file first.
				pathRelativeTo = r.PackagePath
				return ignoreFilesMatcher.readIgnoreFile(path)
			}

			// user specified path is a file rather than a directory.
			// make its path relative to its parent so it can be written to another file.
			pathRelativeTo = filepath.Dir(r.PackagePath)
		}

		// check if we should skip the directory or file
		if info.IsDir() {
			return r.ShouldSkipDir(path, ignoreFilesMatcher)
		}
		if match, err := r.ShouldSkipFile(path, ignoreFilesMatcher); err != nil {
			return err
		} else if !match {
			// skip this file
			return nil
		}

		// get the relative path to file within the package so we can write the files back out
		// to another location.
		path, err = filepath.Rel(pathRelativeTo, path)
		if err != nil {
			return errors.WrapPrefixf(err, pathRelativeTo)
		}

		r.initReaderAnnotations(path, info)
		nodes, err := r.readFile(filepath.Join(pathRelativeTo, path), info)
		if err != nil {
			return errors.WrapPrefixf(err, filepath.Join(pathRelativeTo, path))
		}
		operand = append(operand, nodes...)
		return nil
	})
	return operand, err
}

// readFile reads the ResourceNodes from a file
func (r *LocalPackageReader) readFile(path string, _ os.FileInfo) ([]*yaml.RNode, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	rr := &ByteReader{
		DisableUnwrapping:     true,
		Reader:                f,
		OmitReaderAnnotations: r.OmitReaderAnnotations,
		SetAnnotations:        r.SetAnnotations,
	}
	return rr.Read()
}

// ShouldSkipFile returns true if the file should be skipped
func (r *LocalPackageReader) ShouldSkipFile(path string, matcher *IgnoreFilesMatcher) (bool, error) {
	// check if the file is covered by a .krmignore file.
	if matcher.matchFile(path) {
		return false, nil
	}

	// check if the files are in scope
	for _, g := range r.MatchFilesGlob {
		if match, err := filepath.Match(g, filepath.Base(path)); err != nil {
			return false, errors.Wrap(err)
		} else if match {
			return true, nil
		}
	}
	return false, nil
}

// initReaderAnnotations adds the LocalPackageReader Annotations to r.SetAnnotations
func (r *LocalPackageReader) initReaderAnnotations(path string, _ os.FileInfo) {
	if r.SetAnnotations == nil {
		r.SetAnnotations = map[string]string{}
	}
	if !r.OmitReaderAnnotations {
		r.SetAnnotations[kioutil.PathAnnotation] = path
	}
}

// ShouldSkipDir returns a filepath.SkipDir if the directory should be skipped
func (r *LocalPackageReader) ShouldSkipDir(path string, matcher *IgnoreFilesMatcher) error {
	if r.PackageFileName == "" {
		// If the folder is not a package, but covered by the .krmignore file,
		// we skip it.
		if matcher.matchDir(path) {
			return filepath.SkipDir
		}
		return nil
	}
	// check if this is a subpackage
	_, err := os.Stat(filepath.Join(path, r.PackageFileName))
	if os.IsNotExist(err) {
		// Skip the folder if it is covered by the .krmignore file.
		if matcher.matchDir(path) {
			return filepath.SkipDir
		}
		return nil
	} else if err != nil {
		return errors.Wrap(err)
	}
	if !r.IncludeSubpackages {
		return filepath.SkipDir
	}
	// We don't allow the .krmignore file in a package cause us to skip
	// a subpackage. So if we have found a package file in the folder and
	// we should include subpackages, we don't check the .krmignore file. We
	// do however check whether the package contains a .krmignore file.
	return matcher.readIgnoreFile(path)
}
