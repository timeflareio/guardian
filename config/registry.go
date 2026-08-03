package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

// FieldSpec is one configuration parameter, derived from the Config struct
// tags at init time. There is deliberately no hand-written registry: a field
// added to Config is automatically settable, gettable, env-overridable, and
// documented — the "field exists in struct but not in registry" drift class
// cannot occur.
type FieldSpec struct {
	Key         string // canonical key, e.g. "polling_interval"
	AltKey      string // kebab form, e.g. "polling-interval"
	EnvVar      string // e.g. "GUARDIAN_POLLING_INTERVAL"
	Group       string
	Description string
	IsPath      bool
	// PresenceOnly fields are listed as set/not set rather than by value —
	// for values that are legible to nobody, like a 60-character hash.
	PresenceOnly bool

	fieldIndex int
}

var (
	fieldSpecs  []FieldSpec
	fieldsByKey map[string]*FieldSpec
	groupOrder  []string
)

func init() {
	buildRegistry()
}

func buildRegistry() {
	t := reflect.TypeFor[Config]()
	fieldsByKey = make(map[string]*FieldSpec)
	seenGroups := make(map[string]bool)

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		key := f.Tag.Get("config")
		if key == "" || key == "-" {
			continue
		}
		spec := FieldSpec{
			Key:          key,
			AltKey:       strings.ReplaceAll(key, "_", "-"),
			EnvVar:       "GUARDIAN_" + strings.ToUpper(key),
			Group:        f.Tag.Get("group"),
			Description:  f.Tag.Get("desc"),
			IsPath:       f.Tag.Get("path") == "true",
			PresenceOnly: f.Tag.Get("display") == "presence",
			fieldIndex:   i,
		}
		fieldSpecs = append(fieldSpecs, spec)
		fieldsByKey[key] = &fieldSpecs[len(fieldSpecs)-1]
		if !seenGroups[spec.Group] {
			seenGroups[spec.Group] = true
			groupOrder = append(groupOrder, spec.Group)
		}
	}
}

// findFieldSpec resolves a key in canonical or kebab form.
func findFieldSpec(key string) *FieldSpec {
	if spec, ok := fieldsByKey[key]; ok {
		return spec
	}
	if spec, ok := fieldsByKey[strings.ReplaceAll(key, "-", "_")]; ok {
		return spec
	}
	return nil
}

// Keys returns every canonical config key, sorted.
func Keys() []string {
	keys := make([]string, 0, len(fieldSpecs))
	for _, spec := range fieldSpecs {
		keys = append(keys, spec.Key)
	}
	sort.Strings(keys)
	return keys
}

// GetField returns the string form of a config field.
func (cfg *Config) GetField(key string) (string, error) {
	spec := findFieldSpec(key)
	if spec == nil {
		return "", fmt.Errorf("unknown configuration key: %s", key)
	}
	return spec.get(cfg), nil
}

// SetField parses and assigns a config field from its string form. Field-level
// validation (parse errors, key formats) happens here; cross-field rules live
// in Config.Validate.
func (cfg *Config) SetField(key, value string) error {
	spec := findFieldSpec(key)
	if spec == nil {
		return fmt.Errorf("unknown configuration key: %s", key)
	}
	if err := validateFieldValue(spec.Key, value); err != nil {
		return err
	}
	return spec.set(cfg, value)
}

// ApplyEnvOverrides applies GUARDIAN_<KEY> environment variables on top of the
// current values. Precedence overall: flags > env > file > defaults — this is
// called after Load and before flags are applied.
func (cfg *Config) ApplyEnvOverrides() error {
	for i := range fieldSpecs {
		spec := &fieldSpecs[i]
		if value, ok := os.LookupEnv(spec.EnvVar); ok {
			if err := cfg.SetField(spec.Key, value); err != nil {
				return fmt.Errorf("%s: %w", spec.EnvVar, err)
			}
		}
	}
	return nil
}

// display returns the form a config listing shows. `config get` still answers
// with the stored value: a targeted read is how an operator or a script asks
// for it deliberately, and a hash is not a secret in the first place.
func (spec *FieldSpec) display(cfg *Config) string {
	value := spec.get(cfg)
	if !spec.PresenceOnly {
		return value
	}
	if value == "" {
		return "not set"
	}
	return "set"
}

func (spec *FieldSpec) get(cfg *Config) string {
	v := reflect.ValueOf(cfg).Elem().Field(spec.fieldIndex)
	switch v.Interface().(type) {
	case time.Duration:
		return v.Interface().(time.Duration).String()
	}
	switch v.Kind() {
	case reflect.String:
		return v.String()
	case reflect.Int, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10)
	case reflect.Bool:
		return strconv.FormatBool(v.Bool())
	case reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", v.Interface())
	}
}

func (spec *FieldSpec) set(cfg *Config, value string) error {
	v := reflect.ValueOf(cfg).Elem().Field(spec.fieldIndex)
	if _, isDuration := v.Interface().(time.Duration); isDuration {
		d, err := time.ParseDuration(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("%s must be a duration (e.g. 6s, 1m): %w", spec.Key, err)
		}
		v.SetInt(int64(d))
		return nil
	}
	switch v.Kind() {
	case reflect.String:
		if spec.IsPath {
			value = expandPath(value)
		}
		v.SetString(value)
	case reflect.Int, reflect.Int64:
		n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return fmt.Errorf("%s must be a number: %w", spec.Key, err)
		}
		v.SetInt(n)
	case reflect.Bool:
		b, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return fmt.Errorf("%s must be true or false: %w", spec.Key, err)
		}
		v.SetBool(b)
	case reflect.Float64:
		f, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return fmt.Errorf("%s must be a number: %w", spec.Key, err)
		}
		v.SetFloat(f)
	default:
		return fmt.Errorf("%s has unsupported type %s", spec.Key, v.Kind())
	}
	return nil
}

// validateFieldValue holds the few set-time rules that go beyond type parsing.
func validateFieldValue(key, value string) error {
	switch key {
	case "log_level":
		if value != "" && !isValidLogLevel(value) {
			return fmt.Errorf("invalid log level: %s (valid: debug, info, warn, error)", value)
		}
	case "log_format":
		if value != "" && !isValidLogFormat(value) {
			return fmt.Errorf("invalid log format: %s (valid: console, json)", value)
		}
	case "encryption_public_key":
		if value != "" && len(value) != 64 {
			return fmt.Errorf("encryption public key must be exactly 64 hex characters (32 bytes)")
		}
	case "dashboard_password_hash":
		// Catches the operator who sets this to the password itself, and the
		// env-override path (GUARDIAN_DASHBOARD_PASSWORD_HASH) with it.
		if value != "" {
			return ValidatePasswordHash(value)
		}
	}
	return nil
}

// expandPath expands environment variables and a leading tilde in paths.
func expandPath(path string) string {
	if path == "" {
		return path
	}
	path = os.ExpandEnv(path)
	if strings.HasPrefix(path, "~") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		if path == "~" {
			return homeDir
		}
		if strings.HasPrefix(path, "~/") {
			return filepath.Join(homeDir, path[2:])
		}
	}
	return path
}
