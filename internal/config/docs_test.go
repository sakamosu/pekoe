package config

import (
	"encoding/json"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

// declared walks Global's types: every dotted key ("agents.*.model") and
// section ("agents.*", "animate") config.toml takes.
func declared() (sections, keys []string) {
	t := reflect.TypeOf(Global{})
	for i := range t.NumField() {
		name := tomlName(t.Field(i))
		if name == "" {
			continue
		}
		ft := t.Field(i).Type
		walk := func(prefix string, st reflect.Type) {
			for j := range st.NumField() {
				if n := tomlName(st.Field(j)); n != "" {
					keys = append(keys, prefix+"."+n)
				}
			}
		}
		switch {
		case ft.Kind() == reflect.Struct:
			sections = append(sections, name)
			walk(name, ft)
		case ft.Kind() == reflect.Map && ft.Elem().Kind() == reflect.Struct:
			sections = append(sections, name+".*")
			walk(name+".*", ft.Elem())
		default:
			sections = append(sections, name)
			keys = append(keys, name)
		}
	}
	return sections, keys
}

// Every section and key Global declares has its line in SectionDocs /
// KeyDocs, and no line is left for one that went.
func TestDocsCoverKeys(t *testing.T) {
	sections, keys := declared()
	both := func(what string, want []string, docs map[string]string) {
		for _, k := range want {
			if strings.TrimSpace(docs[k]) == "" {
				t.Errorf("%s %q has no doc", what, k)
			}
		}
		for k := range docs {
			if !slices.Contains(want, k) {
				t.Errorf("%s doc %q: no such %s in Global", what, k, what)
			}
		}
	}
	both("section", sections, SectionDocs)
	both("key", keys, KeyDocs)
	// what Sections (the tips' cover test) knows is what is documented
	var top []string
	for _, s := range sections {
		top = append(top, strings.TrimSuffix(s, ".*"))
	}
	if got := Sections(); !slices.Equal(got, top) {
		t.Errorf("Sections %v, docs %v", got, top)
	}
}

// Shown: the defaults section by section in Global's order, agents by
// name, every key with its doc, the values as Effective has them (a
// switch left unset as true, durations spelled out), the secret hidden,
// and the restart marks where RestartNeeded watches.
func TestShown(t *testing.T) {
	raw := []byte(`
animate = false
[web]
auth_key = "tskey-auth-secret"
allow_users = ["you@github"]
[theme.colors]
primary = "#ff87da"
[agents.zed]
command = "zed"
[agents."a.b"]
command = "ab"
`)
	g, err := ParseGlobal(raw)
	if err != nil {
		t.Fatal(err)
	}
	ss := Shown(g)
	MarkSet(ss, raw)
	var names []string
	byKey := map[string]ShownKey{}
	for _, s := range ss {
		names = append(names, s.Name)
		if s.Doc == "" {
			t.Errorf("section %s: no doc", s.Name)
		}
		for _, k := range s.Keys {
			if k.Doc == "" {
				t.Errorf("%s.%s: no doc", s.Name, k.Key)
			}
			full := s.Name + "." + k.Key
			if s.Name == k.Key {
				full = k.Key
			}
			byKey[full] = k
		}
	}
	wantNames := []string{"http", "web", "gui", "keys", "theme", "mascot", "agents.a.b", "agents.claude", "agents.codex", "agents.opencode", "agents.zed",
		"review", "ports", "tapi", "tips", "sandbox", "animate", "picks", "orchestrator", "notify", "cleanup", "pr"}
	if !slices.Equal(names, wantNames) {
		t.Errorf("sections %v", names)
	}
	b, _ := json.Marshal(ss)
	if strings.Contains(string(b), "tskey") {
		t.Fatalf("the auth key shows: %s", b)
	}
	for key, want := range map[string]any{
		"web.auth_key":          Hidden,
		"http.port":             47777,
		"gui.session_ttl":       "12h",
		"gui.enable":            true,
		"animate":               false,
		"web.confirm_tapi_send": true,
		"agents.claude.sandbox": true,
		"agents.zed.command":    "zed",
		"orchestrator.deliver":  "off",
		"cleanup.stale_after":   "72h",
	} {
		if got := byKey[key].Value; !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %#v, want %#v", key, got, want)
		}
	}
	if got := byKey["web.allow_users"].Value; !reflect.DeepEqual(got, []string{"you@github"}) {
		t.Errorf("allow_users = %#v", got)
	}
	if got := byKey["theme.colors"].Value; !reflect.DeepEqual(got, map[string]string{"primary": "#ff87da"}) {
		t.Errorf("theme.colors = %#v", got)
	}
	if got := byKey["mascot.colors"].Value; !reflect.DeepEqual(got, map[string]string{}) {
		t.Errorf("mascot.colors unset = %#v, want {}", got)
	}
	var set []string
	for k, v := range byKey {
		if v.Set {
			set = append(set, k)
		}
	}
	sort.Strings(set)
	if want := []string{"agents.a.b.command", "agents.zed.command", "animate", "theme.colors", "web.allow_users", "web.auth_key"}; !slices.Equal(set, want) {
		t.Errorf("set %v, want %v", set, want)
	}
	MarkSet(ss, []byte("[web\n"))
	var restart []string
	for k, v := range byKey {
		if v.Restart {
			restart = append(restart, k)
		}
	}
	sort.Strings(restart)
	if want := []string{"http.port", "sandbox.enable", "web.allow_devices", "web.allow_users", "web.auth_key", "web.enable", "web.hostname", "web.state_dir"}; !slices.Equal(restart, want) {
		t.Errorf("restart marks %v, want %v", restart, want)
	}
}

// NeedsRestart says what RestartNeeded does: each key changed alone waits
// for a restart exactly when NeedsRestart names it.
func TestNeedsRestartMatchesRestartNeeded(t *testing.T) {
	_, keys := declared()
	for _, key := range keys {
		base, _ := ParseGlobal(nil)
		next, _ := ParseGlobal(nil)
		if !change(reflect.ValueOf(next).Elem(), strings.Split(strings.Replace(key, "*", "claude", 1), ".")) {
			t.Errorf("%s: could not change it", key)
			continue
		}
		got := len(RestartNeeded(base, next)) > 0
		if want := NeedsRestart(strings.Replace(key, "*", "claude", 1)); got != want {
			t.Errorf("%s: RestartNeeded %v, NeedsRestart %v", key, got, want)
		}
	}
}

// change sets the field at path in v to something other than it was.
func change(v reflect.Value, path []string) bool {
	if len(path) == 0 {
		switch v.Kind() {
		case reflect.String:
			v.SetString(v.String() + "x")
		case reflect.Int:
			v.SetInt(v.Int() + 1)
		case reflect.Bool:
			v.SetBool(!v.Bool())
		case reflect.Pointer: // *bool: unset reads as true
			f := false
			v.Set(reflect.ValueOf(&f))
		case reflect.Slice:
			v.Set(reflect.Append(v, reflect.ValueOf("x")))
		case reflect.Map:
			m := reflect.MakeMap(v.Type())
			m.SetMapIndex(reflect.ValueOf("x"), reflect.Zero(v.Type().Elem()))
			v.Set(m)
		default:
			return false
		}
		return true
	}
	if v.Kind() == reflect.Map { // agents.<name>
		k := reflect.ValueOf(path[0])
		e := reflect.New(v.Type().Elem()).Elem()
		e.Set(v.MapIndex(k))
		if !change(e, path[1:]) {
			return false
		}
		v.SetMapIndex(k, e)
		return true
	}
	t := v.Type()
	for i := range t.NumField() {
		if tomlName(t.Field(i)) == path[0] {
			return change(v.Field(i), path[1:])
		}
	}
	return false
}
