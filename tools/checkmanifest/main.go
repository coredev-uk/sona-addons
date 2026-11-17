// checkmanifest validates add-on folders against Sona's manifest rules.
//
//	go run ./tools/checkmanifest dist/native/* dist/wasm/*
//
// Each argument is an add-on folder (the form Sona installs): its
// addon.json is parsed as Sona parses it, its ID must be the folder's name,
// and its command must be a regular file inside the folder, executable for a
// native add-on. The rules are Sona's own (internal/addons/manifest.go and
// contributions.go, and docs/addons.md, "Manifest fields"), copied here
// because that package is internal to Sona: if Sona's rules change, so must
// this file. The authority is always `sona addons dev <folder>`.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxManifestBytes = 64 << 10
	maxNameRunes     = 60
	maxDescRunes     = 300
	maxLabelRunes    = 60
	maxDetailRunes   = 200
	maxCommandBytes  = 200
)

// permissions are protocol v2's, and "network" (the permission to reach the
// internet from the sandbox).
var permissions = []string{
	"playback.read", "playback.control", "queue.read", "lyrics.read", "search",
	"open", "volume", "like", "private.read", "network",
}

var (
	idExpr      = regexp.MustCompile(`^[a-z][a-z0-9-]{1,39}$`)
	versionExpr = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
	localIDExpr = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	localeExpr  = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8}){0,2}$`)
	valueExpr   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,40}$`)
)

type manifest struct {
	ID            string                       `json:"id"`
	Name          string                       `json:"name"`
	Version       string                       `json:"version"`
	Description   string                       `json:"description,omitempty"`
	Runtime       string                       `json:"runtime,omitempty"`
	Command       string                       `json:"command"`
	Permissions   []string                     `json:"permissions"`
	Contributions contributions                `json:"contributions,omitempty"`
	Strings       map[string]map[string]string `json:"strings,omitempty"`
}

type contributions struct {
	Actions []struct {
		ID    string   `json:"id"`
		Label string   `json:"label"`
		Kinds []string `json:"kinds"`
		Icon  string   `json:"icon,omitempty"`
	} `json:"actions,omitempty"`
	Commands []struct {
		ID       string `json:"id"`
		Label    string `json:"label"`
		Keywords string `json:"keywords,omitempty"`
		Icon     string `json:"icon,omitempty"`
	} `json:"commands,omitempty"`
	Settings []setting `json:"settings,omitempty"`
}

type setting struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Default     string `json:"default,omitempty"`
	Options     []struct {
		Value string `json:"value"`
		Label string `json:"label"`
	} `json:"options,omitempty"`
}

// problem is a refusal code of Sona's, with what it was about.
type problem struct{ code, detail string }

func (p *problem) Error() string { return p.code + ": " + p.detail }

func refuse(code, format string, args ...any) error {
	return &problem{code, fmt.Sprintf(format, args...)}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: checkmanifest <add-on folder>...")
		os.Exit(2)
	}
	failed := false
	for _, dir := range os.Args[1:] {
		if err := checkFolder(dir); err != nil {
			fmt.Printf("FAIL %s: %v\n", dir, err)
			failed = true
			continue
		}
		fmt.Printf("ok   %s\n", dir)
	}
	if failed {
		os.Exit(1)
	}
}

// checkFolder is what Sona's scanner checks of an installed folder, except
// owners and modes.
func checkFolder(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return refuse("unreadable", "%v", err)
	}
	if !info.IsDir() {
		return refuse("unreadable", "not a folder (or a link)")
	}
	data, err := os.ReadFile(filepath.Join(dir, "addon.json"))
	if err != nil {
		return refuse("unreadable", "%v", err)
	}
	m, err := parse(data)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return refuse("unreadable", "%v", err)
	}
	if !strings.EqualFold(filepath.Base(abs), m.ID) {
		return refuse("id_mismatch", "the folder is %q, the ID %q", filepath.Base(abs), m.ID)
	}
	root, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return refuse("unreadable", "%v", err)
	}
	cmd, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(m.Command)))
	if err != nil {
		return refuse("no_executable", "%v", err)
	}
	if rel, err := filepath.Rel(root, cmd); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return refuse("executable_escapes", "%s", m.Command)
	}
	st, err := os.Stat(cmd)
	switch {
	case err != nil || !st.Mode().IsRegular():
		return refuse("no_executable", "%s is not a regular file", m.Command)
	case m.Runtime != "wasm" && st.Mode().Perm()&0o111 == 0:
		return refuse("no_executable", "%s is not executable", m.Command)
	}
	return nil
}

// parse reads and validates a manifest's text (Sona's ParseManifest).
func parse(data []byte) (manifest, error) {
	if len(data) > maxManifestBytes {
		return manifest{}, refuse("manifest_too_large", "%d bytes", len(data))
	}
	var m manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return manifest{}, refuse("manifest_invalid", "%v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return manifest{}, refuse("manifest_invalid", "text after the manifest")
	}
	return m, m.validate()
}

func (m *manifest) validate() error {
	if !idExpr.MatchString(m.ID) {
		return refuse("bad_id", "%q", m.ID)
	}
	if strings.HasPrefix(m.ID, "sona") || slices.Contains([]string{"lastfm", "listenbrainz", "discord"}, m.ID) {
		return refuse("reserved_id", "%q", m.ID)
	}
	if err := plainText(m.Name, 1, maxNameRunes); err != nil {
		return refuse("bad_name", "name: %v", err)
	}
	if m.Description != "" {
		if err := plainText(m.Description, 0, maxDescRunes); err != nil {
			return refuse("bad_name", "description: %v", err)
		}
	}
	if len(m.Version) > 40 || !versionExpr.MatchString(m.Version) {
		return refuse("bad_version", "%q", m.Version)
	}
	if err := checkCommand(m.Command); err != nil {
		return err
	}
	if m.Permissions == nil {
		return refuse("manifest_invalid", "permissions is required (it may be empty)")
	}
	if len(m.Permissions) > len(permissions) {
		return refuse("bad_permission", "%d permissions", len(m.Permissions))
	}
	for i, p := range m.Permissions {
		if !slices.Contains(permissions, p) || slices.Contains(m.Permissions[:i], p) {
			return refuse("bad_permission", "%q", p)
		}
	}
	switch m.Runtime {
	case "", "native":
	case "wasm":
		// WASI has no sockets: a wasm add-on with network is refused.
		if slices.Contains(m.Permissions, "network") {
			return refuse("bad_permission", "a wasm add-on has no network")
		}
		if !strings.HasSuffix(m.Command, ".wasm") {
			return refuse("bad_command", "a wasm add-on's command is a .wasm file")
		}
	default:
		return refuse("manifest_invalid", "runtime %q", m.Runtime)
	}
	if err := m.Contributions.validate(); err != nil {
		return err
	}
	return m.validateStrings()
}

func plainText(s string, min, max int) error {
	n := utf8.RuneCountInString(s)
	if !utf8.ValidString(s) || n < min || n > max {
		return fmt.Errorf("not %d to %d characters of text", min, max)
	}
	if s != strings.TrimSpace(s) {
		return errors.New("spaces at an edge")
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.In(r, unicode.Zl, unicode.Zp) {
			return fmt.Errorf("control or format character U+%04X", r)
		}
	}
	return nil
}

func checkCommand(c string) error {
	bad := func() error { return refuse("bad_command", "%q", c) }
	if c == "" || len(c) > maxCommandBytes || !utf8.ValidString(c) {
		return bad()
	}
	if strings.ContainsAny(c, "\\:\x00") || strings.HasPrefix(c, "/") {
		return bad()
	}
	for _, r := range c {
		if unicode.IsControl(r) {
			return bad()
		}
	}
	for el := range strings.SplitSeq(c, "/") {
		if el == "" || el == "." || el == ".." {
			return bad()
		}
	}
	return nil
}

func (c contributions) validate() error {
	if len(c.Actions) > 8 || len(c.Commands) > 8 || len(c.Settings) > 16 ||
		len(c.Actions)+len(c.Commands)+len(c.Settings) > 32 {
		return refuse("bad_contribution", "too many contributions")
	}
	ids := map[string]bool{}
	bad := func(format string, args ...any) error { return refuse("bad_contribution", format, args...) }
	for _, a := range c.Actions {
		if !localIDExpr.MatchString(a.ID) || ids[a.ID] {
			return bad("action id %q", a.ID)
		}
		ids[a.ID] = true
		if err := plainText(a.Label, 1, maxLabelRunes); err != nil {
			return bad("action %s label: %v", a.ID, err)
		}
		if len(a.Kinds) == 0 || len(a.Kinds) > 4 {
			return bad("action %s kinds", a.ID)
		}
		for i, k := range a.Kinds {
			if !slices.Contains([]string{"song", "album", "artist", "playlist"}, k) || slices.Contains(a.Kinds[:i], k) {
				return bad("action %s kind %q", a.ID, k)
			}
		}
		if a.Icon != "" && !localIDExpr.MatchString(a.Icon) {
			return bad("action %s icon", a.ID)
		}
	}
	for _, a := range c.Commands {
		if !localIDExpr.MatchString(a.ID) || ids[a.ID] {
			return bad("command id %q", a.ID)
		}
		ids[a.ID] = true
		if err := plainText(a.Label, 1, maxLabelRunes); err != nil {
			return bad("command %s label: %v", a.ID, err)
		}
		if a.Keywords != "" {
			if err := plainText(a.Keywords, 1, maxLabelRunes); err != nil {
				return bad("command %s keywords: %v", a.ID, err)
			}
		}
		if a.Icon != "" && !localIDExpr.MatchString(a.Icon) {
			return bad("command %s icon", a.ID)
		}
	}
	rows := map[string]bool{}
	for _, s := range c.Settings {
		if !localIDExpr.MatchString(s.ID) || rows[s.ID] {
			return bad("setting id %q", s.ID)
		}
		rows[s.ID] = true
		if err := plainText(s.Label, 1, maxLabelRunes); err != nil {
			return bad("setting %s label: %v", s.ID, err)
		}
		if s.Description != "" {
			if err := plainText(s.Description, 1, maxDetailRunes); err != nil {
				return bad("setting %s description: %v", s.ID, err)
			}
		}
		if err := s.validateKind(); err != nil {
			return err
		}
	}
	return nil
}

func (s setting) validateKind() error {
	bad := func(format string, args ...any) error {
		return refuse("bad_contribution", "setting %s: "+format, append([]any{s.ID}, args...)...)
	}
	if s.Kind != "choice" && len(s.Options) != 0 {
		return bad("options on a %s row", s.Kind)
	}
	switch s.Kind {
	case "toggle":
		if s.Default != "" && s.Default != "true" && s.Default != "false" {
			return bad("default %q", s.Default)
		}
	case "text":
		if len(s.Default) > maxDetailRunes {
			return bad("default too long")
		}
		if s.Default != "" {
			if err := plainText(s.Default, 1, maxDetailRunes); err != nil {
				return bad("default: %v", err)
			}
		}
	case "secret":
		if s.Default != "" {
			return bad("a secret has no default")
		}
	case "choice":
		if len(s.Options) < 2 || len(s.Options) > 16 {
			return bad("%d options", len(s.Options))
		}
		seen := map[string]bool{}
		for _, o := range s.Options {
			if !valueExpr.MatchString(o.Value) || seen[o.Value] {
				return bad("option value %q", o.Value)
			}
			seen[o.Value] = true
			if err := plainText(o.Label, 1, maxLabelRunes); err != nil {
				return bad("option %s label: %v", o.Value, err)
			}
		}
		if s.Default != "" && !seen[s.Default] {
			return bad("default %q is not an option", s.Default)
		}
	default:
		return bad("kind %q", s.Kind)
	}
	return nil
}

func (m *manifest) validateStrings() error {
	if len(m.Strings) > 40 {
		return refuse("bad_strings", "%d locales", len(m.Strings))
	}
	known := map[string]bool{}
	for _, a := range m.Contributions.Actions {
		known["action."+a.ID] = true
	}
	for _, a := range m.Contributions.Commands {
		known["command."+a.ID] = true
		if a.Keywords != "" {
			known["command."+a.ID+".keywords"] = true
		}
	}
	for _, r := range m.Contributions.Settings {
		known["setting."+r.ID] = true
		known["setting."+r.ID+".description"] = r.Description != ""
		for _, o := range r.Options {
			known["option."+r.ID+"."+o.Value] = true
		}
	}
	for locale, labels := range m.Strings {
		if !localeExpr.MatchString(locale) || len(labels) > 100 {
			return refuse("bad_strings", "locale %q", locale)
		}
		for k, v := range labels {
			if !known[k] {
				return refuse("bad_strings", "%s: no such label %q", locale, k)
			}
			if err := plainText(v, 1, maxDetailRunes); err != nil {
				return refuse("bad_strings", "%s %s: %v", locale, k, err)
			}
		}
	}
	return nil
}
