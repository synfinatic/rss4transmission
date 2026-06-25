package main

import (
	"regexp"
	"sort"
)

// LabelDef is the config definition for a single label extraction rule.
type LabelDef struct {
	Regexp    string            `koanf:"Regexp"`
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

func (es *ExtractorSet) compile() {
	if es.isCompiled {
		return
	}
	es.compiledLabels = make(map[string]*compiledLabel, len(es.Labels))
	for name, def := range es.Labels {
		cl := &compiledLabel{}
		var err error
		if cl.re, err = regexp.Compile(def.Regexp); err != nil {
			log.WithError(err).Fatalf("label %q: invalid Regexp: %s", name, def.Regexp)
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
				log.WithError(err).Fatalf("label %q: invalid Normalize pattern: %s", name, p)
			}
			cl.normalize = append(cl.normalize, normalizeRule{re: r, value: def.Normalize[p]})
		}
		es.compiledLabels[name] = cl
	}
	es.isCompiled = true
}

// ExtractLabels extracts labels from s. Labels whose regex does not match are
// omitted from the result. After extraction, the first matching Normalize rule
// (sorted by pattern string) maps the raw match to a canonical value.
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

// ExtractFromFiles extracts labels from each file name. Files that produce no
// label matches are omitted from the result.
func (es *ExtractorSet) ExtractFromFiles(fileNames []string) []map[string]string {
	result := make([]map[string]string, 0, len(fileNames))
	for _, fn := range fileNames {
		labels := es.ExtractLabels(fn)
		if len(labels) > 0 {
			result = append(result, labels)
		}
	}
	return result
}
