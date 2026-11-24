package prove

import (
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// FileClass semantically partitions incident files into product code, tests, and test support (§5.1).
type FileClass string

const (
	ClassProduct FileClass = "product"
	ClassTest    FileClass = "test"
	ClassSupport FileClass = "support"
)

// Classifier separates test and test-support files from product code when reverting a fix.
type Classifier struct {
	SupportGlobs    []string
	ExplicitProduct map[string]bool
	ExplicitTest    map[string]bool
	ExplicitSupport map[string]bool
}

// NewClassifier creates a Classifier with default and user-specified rules.
func NewClassifier(supportGlobs, explicitProduct, explicitTest, explicitSupport []string) Classifier {
	c := Classifier{
		SupportGlobs:    supportGlobs,
		ExplicitProduct: toSet(explicitProduct),
		ExplicitTest:    toSet(explicitTest),
		ExplicitSupport: toSet(explicitSupport),
	}
	if len(c.SupportGlobs) == 0 {
		c.SupportGlobs = []string{
			"**/testdata/**",
			"**/conftest.py",
			"**/__snapshots__/**",
			"**/__mocks__/**",
			"**/fixtures/**",
			"**/*.golden",
		}
	}
	return c
}

// Classify returns whether relPath belongs to product code, tests, or test support.
// Explicit overrides take precedence over glob and naming heuristics (§5.1).
func (c Classifier) Classify(relPath string) FileClass {
	clean := normalizePath(relPath)
	if c.ExplicitProduct[clean] {
		return ClassProduct
	}
	if c.ExplicitTest[clean] {
		return ClassTest
	}
	if c.ExplicitSupport[clean] {
		return ClassSupport
	}
	for _, g := range c.SupportGlobs {
		if ok, err := doublestar.Match(filepath.ToSlash(g), clean); err == nil && ok {
			return ClassSupport
		}
	}
	if isTestFile(clean) {
		return ClassTest
	}
	return ClassProduct
}

func isTestFile(cleanPath string) bool {
	base := filepath.Base(cleanPath)
	switch {
	case strings.HasSuffix(base, "_test.go"):
		return true
	case strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py"):
		return true
	case strings.HasSuffix(base, "_test.py"):
		return true
	case strings.HasSuffix(base, ".test.ts"), strings.HasSuffix(base, ".spec.ts"),
		strings.HasSuffix(base, ".test.tsx"), strings.HasSuffix(base, ".spec.tsx"),
		strings.HasSuffix(base, ".test.js"), strings.HasSuffix(base, ".spec.js"),
		strings.HasSuffix(base, ".test.jsx"), strings.HasSuffix(base, ".spec.jsx"):
		return true
	}
	return false
}

func toSet(items []string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, it := range items {
		m[normalizePath(it)] = true
	}
	return m
}

func normalizePath(p string) string {
	return filepath.ToSlash(strings.TrimPrefix(filepath.Clean(p), "./"))
}
