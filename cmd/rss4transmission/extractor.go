package main

import (
	"fmt"
	"regexp"
	"sort"
)

// LabelDef is the config definition for a single label extraction rule.
type LabelDef struct {
	Regexp    string            `koanf:"Regexp"`
	Default   string            `koanf:"Default"`
	Normalize map[string]string `koanf:"Normalize"`
}

// ExtractorSet is a named collection of label definitions.
type ExtractorSet struct {
	Labels map[string]LabelDef `koanf:"Labels"`

	isCompiled     bool
	compiledLabels map[string]*compiledLabel
}

type compiledLabel struct {
	re        *regexp.Regexp
	normalize []normalizeRule
}

type normalizeRule struct {
	re    *regexp.Regexp
	value string
}

// validateLabelRegexp checks that re has exactly one capture group.
func validateLabelRegexp(name, pattern string, re *regexp.Regexp) error {
	if n := re.NumSubexp(); n != 1 {
		return fmt.Errorf("label %q: Regexp must have exactly one capture group, got %d: %s", name, n, pattern)
	}
	return nil
}

// Compile builds the regexes for every label. It returns an error rather than
// calling log.Fatalf so that loadConfig can reject a bad pattern and leave the
// previously running config in effect, which matters on a live config reload.
//
// loadConfig calls this for every set at load time, so by the time extraction
// runs the set is already compiled and validated.
func (es *ExtractorSet) Compile() error {
	if es.isCompiled {
		return nil
	}
	compiled := make(map[string]*compiledLabel, len(es.Labels))
	for name, def := range es.Labels {
		cl := &compiledLabel{}
		var err error
		if cl.re, err = regexp.Compile(def.Regexp); err != nil {
			return fmt.Errorf("label %q: invalid Regexp %q: %w", name, def.Regexp, err)
		}
		if err = validateLabelRegexp(name, def.Regexp, cl.re); err != nil {
			return err
		}
		// Sort normalize patterns for deterministic order.
		patterns := make([]string, 0, len(def.Normalize))
		for p := range def.Normalize {
			patterns = append(patterns, p)
		}
		sort.Strings(patterns)
		for _, p := range patterns {
			r, err := regexp.Compile(p)
			if err != nil {
				return fmt.Errorf("label %q: invalid Normalize pattern %q: %w", name, p, err)
			}
			cl.normalize = append(cl.normalize, normalizeRule{re: r, value: def.Normalize[p]})
		}
		compiled[name] = cl
	}
	// Published only once every label compiled, so a failed Compile leaves the
	// set exactly as it was rather than half-built.
	es.compiledLabels = compiled
	es.isCompiled = true
	return nil
}

// compile is the lazy path used by extraction. Compile has normally already
// run at config load; a set built directly (in tests, or by a caller that
// skipped loadConfig) compiles here instead. A bad pattern extracts nothing
// rather than ending the process.
func (es *ExtractorSet) compile() {
	if err := es.Compile(); err != nil {
		log.WithError(err).Error("Unable to compile extractor labels")
	}
}

// ExtractLabels extracts labels from s. Labels whose regex does not match are
// omitted from the result. After extraction, the first matching Normalize rule
// (sorted by pattern string) maps the raw match to a canonical value.
// Defaults are NOT applied here; use Defaults() and apply them at coverage time
// so that a file's absent label never overrides a title's explicit value.
func (es *ExtractorSet) ExtractLabels(s string) map[string]string {
	es.compile()
	result := make(map[string]string)
	for name, cl := range es.compiledLabels {
		match := cl.re.FindStringSubmatch(s)
		if len(match) < 2 {
			continue
		}
		raw := match[1]
		for _, rule := range cl.normalize {
			if rule.re.MatchString(raw) {
				raw = rule.value
				break
			}
		}
		result[name] = raw
	}
	return result
}

// Defaults returns label name → default value for all labels with a non-empty Default.
func (es *ExtractorSet) Defaults() map[string]string {
	result := make(map[string]string)
	for name, def := range es.Labels {
		if def.Default != "" {
			result[name] = def.Default
		}
	}
	return result
}

// hasAnyRegexMatch reports whether any label's Regexp matches s.
func (es *ExtractorSet) hasAnyRegexMatch(s string) bool {
	es.compile()
	for _, cl := range es.compiledLabels {
		if len(cl.re.FindStringSubmatch(s)) >= 2 {
			return true
		}
	}
	return false
}

// ExtractFromFiles extracts labels from each file name. Files where no label
// Regexp matches are omitted. Defaults are not applied here — they are applied
// at coverage time via Defaults() so a file's absent label never silently
// overrides an explicit value from the torrent title.
func (es *ExtractorSet) ExtractFromFiles(fileNames []string) []map[string]string {
	result := make([]map[string]string, 0, len(fileNames))
	for _, fn := range fileNames {
		if !es.hasAnyRegexMatch(fn) {
			continue
		}
		result = append(result, es.ExtractLabels(fn))
	}
	return result
}
