// Package patterns implements a small JSON-based DSL that lets operators
// describe their own anti-patterns and have the findings engine surface
// them automatically. The shipped pattern catalog lives in patterns/builtin/
// and gets compiled in via go:embed; sites can also load their own from
// a directory at startup.
//
// Design goals:
//   - Cheap to author — author writes JSON, no Go.
//   - Cheap to scan — regex compiles once at load time; matching is per
//     dump-line, not per pattern × per frame.
//   - Conservative — a pattern that hits its conditions emits a Finding
//     with the author-supplied severity, headline, detail, remediation.
//     No surprise behaviour.
//
// Each rule has three condition families:
//
//   stack_includes_any   — regex(es); a thread "matches" when any of its
//                          frame.Sig() values matches the regex.
//   states               — required Thread.State (empty means "any").
//   min_threads          — threshold; rule fires only when >= N threads
//                          in one dump match.
package patterns

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/roshandubey-cloud/utilities/threaddump-analyzer/internal/parser"
)

//go:embed builtin/*.json
var builtinFS embed.FS

// Rule is the on-disk pattern definition. We compile regex patterns at load
// time and stash them in Compiled to keep match-time cheap.
type Rule struct {
	ID                 string   `json:"id"`
	Kind               string   `json:"kind"`
	Severity           string   `json:"severity"` // critical/high/medium/info
	Confidence         int      `json:"confidence"`
	Headline           string   `json:"headline"` // {N} placeholder for match count
	Detail             string   `json:"detail"`
	Remediation        string   `json:"remediation"`
	StackIncludesAny   []string `json:"stack_includes_any"`
	NameIncludesAny    []string `json:"name_includes_any,omitempty"`
	States             []string `json:"states,omitempty"`
	MinThreads         int      `json:"min_threads"`

	stackRe []*regexp.Regexp
	nameRe  []*regexp.Regexp
	stateOK map[parser.ThreadState]bool
}

func (r *Rule) compile() error {
	for _, p := range r.StackIncludesAny {
		re, err := regexp.Compile(p)
		if err != nil {
			return fmt.Errorf("rule %s: stack_includes_any %q: %w", r.ID, p, err)
		}
		r.stackRe = append(r.stackRe, re)
	}
	for _, p := range r.NameIncludesAny {
		re, err := regexp.Compile(p)
		if err != nil {
			return fmt.Errorf("rule %s: name_includes_any %q: %w", r.ID, p, err)
		}
		r.nameRe = append(r.nameRe, re)
	}
	r.stateOK = map[parser.ThreadState]bool{}
	for _, s := range r.States {
		r.stateOK[parser.ThreadState(s)] = true
	}
	if r.MinThreads <= 0 {
		r.MinThreads = 1
	}
	if r.Confidence <= 0 {
		r.Confidence = 70
	}
	return nil
}

// matches reports whether one thread satisfies every condition family. We
// don't require BOTH stack and name regex matches — either family is enough
// when the other is empty. States, when set, is a hard filter.
func (r *Rule) matches(t parser.Thread) bool {
	if len(r.stateOK) > 0 && !r.stateOK[t.State] {
		return false
	}
	stackOK := len(r.stackRe) == 0
	if !stackOK {
		for _, f := range t.Frames {
			sig := f.Sig()
			for _, re := range r.stackRe {
				if re.MatchString(sig) {
					stackOK = true
					break
				}
			}
			if stackOK {
				break
			}
		}
	}
	if !stackOK {
		return false
	}
	if len(r.nameRe) > 0 {
		ok := false
		for _, re := range r.nameRe {
			if re.MatchString(t.Name) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// Match is one rule's verdict against one dump. Threads is the list of
// thread names that satisfied the rule's conditions.
type Match struct {
	Rule    *Rule    `json:"-"`
	Headline string  `json:"headline"`
	Detail   string  `json:"detail"`
	Remediation string `json:"remediation"`
	Severity string  `json:"severity"`
	Confidence int   `json:"confidence"`
	Kind     string  `json:"kind"`
	ID       string  `json:"id"`
	Threads  []string `json:"threads"`
}

// Registry holds the loaded rules. Thread-safe — load once at startup,
// then call Apply from any goroutine.
type Registry struct {
	rules []*Rule
}

// Apply runs every rule against the dump and returns the matches. Order is
// stable: rules in load order, threads in dump order.
func (r *Registry) Apply(d *parser.Dump) []Match {
	out := []Match{}
	for _, rule := range r.rules {
		hits := []string{}
		for _, t := range d.Threads {
			if rule.matches(t) {
				hits = append(hits, t.Name)
			}
		}
		if len(hits) < rule.MinThreads {
			continue
		}
		hl := strings.ReplaceAll(rule.Headline, "{N}", fmt.Sprintf("%d", len(hits)))
		out = append(out, Match{
			Rule:        rule,
			ID:          rule.ID,
			Kind:        rule.Kind,
			Severity:    rule.Severity,
			Confidence:  rule.Confidence,
			Headline:    hl,
			Detail:      rule.Detail,
			Remediation: rule.Remediation,
			Threads:     hits,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return sevRank(out[i].Severity) > sevRank(out[j].Severity)
		}
		if out[i].Confidence != out[j].Confidence {
			return out[i].Confidence > out[j].Confidence
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Rules returns a shallow copy of the loaded rule list. Used by the API to
// surface what's installed.
func (r *Registry) Rules() []*Rule {
	out := make([]*Rule, len(r.rules))
	copy(out, r.rules)
	return out
}

// Load reads every *.json under dir as a single Rule (one rule per file).
// dir may be empty to skip; the builtin embed is always loaded regardless.
func Load(dir string) (*Registry, error) {
	r := &Registry{}
	if err := r.loadEmbedded(); err != nil {
		return nil, err
	}
	if dir == "" {
		return r, nil
	}
	if err := r.loadDir(dir); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Registry) loadEmbedded() error {
	return fs.WalkDir(builtinFS, "builtin", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".json") {
			return err
		}
		data, err := builtinFS.ReadFile(p)
		if err != nil {
			return err
		}
		return r.add(p, data)
	})
}

func (r *Registry) loadDir(dir string) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil // missing dir is fine
			}
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".json") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return r.add(p, data)
	})
}

func (r *Registry) add(source string, data []byte) error {
	var rule Rule
	if err := json.Unmarshal(data, &rule); err != nil {
		return fmt.Errorf("%s: %w", source, err)
	}
	if rule.ID == "" {
		return fmt.Errorf("%s: rule missing id", source)
	}
	if err := rule.compile(); err != nil {
		return err
	}
	r.rules = append(r.rules, &rule)
	return nil
}

func sevRank(s string) int {
	switch s {
	case "critical":
		return 4
	case "high":
		return 3
	case "medium":
		return 2
	case "info":
		return 1
	}
	return 0
}
