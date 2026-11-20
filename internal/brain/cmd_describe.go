package brain

import (
	"fmt"
	"io"
	"strconv"
	"strings"
)

// `os-brain describe` maintains this project's entry in the shared index: enough for
// another project to work out whether to come asking, and never any memory contents. The
// fields live in brain.json, because they are configuration and configuration is local on
// purpose; the index row is refreshed from there.

type DescribeArgs struct {
	Text       string
	Does       string
	Provides   string
	Keywords   string
	Stack      string
	Status     string
	Visibility string
	Sync       bool
	JSON       bool
}

// SpecStaleDays: how long an index entry stays believable without being restamped.
const SpecStaleDays = 120

var Visibilities = []string{"described", "listed"}

func (s *Store) Describe(w io.Writer, a DescribeArgs) error {
	cfg, touched, err := ApplySpec(s.Env, a)
	if err != nil {
		return err
	}
	if touched {
		s.Env.Config = cfg
		s.RegisterProject()
	}
	vis := s.visibility()
	visNote := "  (other projects can read the spec above)"
	if vis == "listed" {
		visNote = "  (other projects see only that this project exists)"
	}
	human, obj := RenderSpec(s.Env, cfg, vis+visNote)
	return Out(w, a.JSON, human, obj)
}

// ApplySpec writes the described fields into brain.json. It returns the config it wrote
// and whether anything changed; --sync counts as a change, because restamping the date is
// its whole job.
func ApplySpec(env Env, a DescribeArgs) (Config, bool, error) {
	cfg := env.Config
	if a.Visibility != "" && !contains(Visibilities, a.Visibility) {
		return cfg, false, fmt.Errorf("--visibility must be one of %s", strings.Join(Visibilities, ", "))
	}
	if cfg.Context == nil {
		cfg.Context = map[string]string{}
	}
	if cfg.Spec == nil {
		cfg.Spec = map[string]string{}
	}
	touched := false
	// about, stack and status live under "context" in brain.json; the rest under "spec".
	for _, f := range []struct {
		val  string
		spec bool
		key  string
	}{
		{a.Text, false, "about"}, {a.Stack, false, "stack"}, {a.Status, false, "status"},
		{a.Does, true, "does"}, {a.Provides, true, "provides"},
		{a.Keywords, true, "keywords"}, {a.Visibility, true, "visibility"},
	} {
		if f.val == "" {
			continue
		}
		if err := CheckLine("the "+f.key, f.val); err != nil {
			return cfg, false, err
		}
		if err := GuardTextNoOverride(f.val); err != nil {
			return cfg, false, err
		}
		if f.spec {
			cfg.Spec[f.key] = f.val
		} else {
			cfg.Context[f.key] = f.val
		}
		touched = true
	}
	if touched || a.Sync {
		cfg.Spec["updated_at"] = strconv.FormatInt(Now(), 10)
		if err := SaveConfig(env.Here, cfg); err != nil {
			return cfg, false, err
		}
	}
	return cfg, touched || a.Sync, nil
}

// RenderSpec is the human block and the JSON object for `describe`. visLine says who can
// see the spec.
func RenderSpec(env Env, cfg Config, visLine string) (string, map[string]interface{}) {
	age, _ := strconv.ParseInt(cfg.Spec["updated_at"], 10, 64)
	human := specFieldLines(env, cfg, visLine, age) + specAdvice(cfg, age)
	return human, specJSON(env, cfg)
}

func specFieldLines(env Env, cfg Config, visLine string, age int64) string {
	get := func(m map[string]string, k string) string {
		if v := m[k]; v != "" {
			return v
		}
		return "(not set)"
	}
	updated := "never"
	if age > 0 {
		updated = ISO(age)
	}
	return strings.Join([]string{
		"  project   " + env.Project,
		"  root      " + env.ProjectRoot(),
		"  about     " + get(cfg.Context, "about"),
		"  does      " + get(cfg.Spec, "does"),
		"  provides  " + get(cfg.Spec, "provides"),
		"  stack     " + get(cfg.Context, "stack"),
		"  keywords  " + get(cfg.Spec, "keywords"),
		"  status    " + get(cfg.Context, "status"),
		"  visible   " + visLine,
		"  updated   " + updated,
	}, "\n")
}

// specAdvice asks for the fields other projects search on when they are missing, or for a
// restamp when the spec is older than SpecStaleDays.
func specAdvice(cfg Config, age int64) string {
	var missing []string
	for _, m := range []struct{ key, val string }{
		{"about", cfg.Context["about"]}, {"does", cfg.Spec["does"]}, {"stack", cfg.Context["stack"]},
	} {
		if m.val == "" {
			missing = append(missing, m.key)
		}
	}
	if len(missing) > 0 {
		return "\n\n  Other projects search this index before solving a problem twice. " +
			"Missing: " + strings.Join(missing, ", ") + ".\n" +
			`  os-brain describe "<one sentence>" --does "<what it is for>" --provides "<what others can use>" \` +
			"\n" + `      --stack "<languages, stores>" --keywords "honeypot, security, go"`
	}
	if age > 0 && Now()-age > SpecStaleDays*86400 {
		return fmt.Sprintf("\n\n  This spec is %d days old. If it still fits, "+
			"`os-brain describe --sync` restamps it.", (Now()-age)/86400)
	}
	return ""
}

func specJSON(env Env, cfg Config) map[string]interface{} {
	obj := map[string]interface{}{"project": env.Project, "root": env.ProjectRoot()}
	for k, v := range cfg.Context {
		obj[k] = v
	}
	for k, v := range cfg.Spec {
		if k == "updated_at" {
			// brain.json stores it as a string; the JSON reports it as the epoch number,
			// so a consumer can compare it without parsing.
			n, _ := strconv.ParseInt(v, 10, 64)
			obj[k] = n
			continue
		}
		obj[k] = v
	}
	return obj
}
