package main

import (
	"os"
	"path/filepath"
	"testing"
)

const good = `{"id":"demo","name":"Demo","version":"1.0.0","command":"bin/demo","permissions":["playback.read","network"]}`

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		name, text, code string
	}{
		{"good", good, ""},
		{"empty permissions", `{"id":"demo","name":"Demo","version":"1.0.0","command":"x","permissions":[]}`, ""},
		{"unknown field", `{"id":"demo","name":"Demo","version":"1.0.0","command":"x","permissions":[],"extra":1}`, "manifest_invalid"},
		{"missing permissions", `{"id":"demo","name":"Demo","version":"1.0.0","command":"x"}`, "manifest_invalid"},
		{"reserved", `{"id":"discord","name":"D","version":"1.0.0","command":"x","permissions":[]}`, "reserved_id"},
		{"sona prefix", `{"id":"sona-x","name":"D","version":"1.0.0","command":"x","permissions":[]}`, "reserved_id"},
		{"bad id", `{"id":"Demo","name":"D","version":"1.0.0","command":"x","permissions":[]}`, "bad_id"},
		{"bad version", `{"id":"demo","name":"D","version":"1.0","command":"x","permissions":[]}`, "bad_version"},
		{"escaping command", `{"id":"demo","name":"D","version":"1.0.0","command":"../x","permissions":[]}`, "bad_command"},
		{"unknown permission", `{"id":"demo","name":"D","version":"1.0.0","command":"x","permissions":["root"]}`, "bad_permission"},
		{"repeated permission", `{"id":"demo","name":"D","version":"1.0.0","command":"x","permissions":["search","search"]}`, "bad_permission"},
		{"wasm", `{"id":"demo","name":"D","version":"1.0.0","runtime":"wasm","command":"x.wasm","permissions":[]}`, ""},
		{"wasm with network", `{"id":"demo","name":"D","version":"1.0.0","runtime":"wasm","command":"x.wasm","permissions":["network"]}`, "bad_permission"},
		{"wasm command", `{"id":"demo","name":"D","version":"1.0.0","runtime":"wasm","command":"x","permissions":[]}`, "bad_command"},
		{"unknown runtime", `{"id":"demo","name":"D","version":"1.0.0","runtime":"jvm","command":"x","permissions":[]}`, "manifest_invalid"},
		{"string for nothing", `{"id":"demo","name":"D","version":"1.0.0","command":"x","permissions":[],"strings":{"de":{"action.x":"X"}}}`, "bad_strings"},
		{"secret with default", `{"id":"demo","name":"D","version":"1.0.0","command":"x","permissions":[],"contributions":{"settings":[{"id":"t","kind":"secret","label":"T","default":"x"}]}}`, "bad_contribution"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse([]byte(tc.text))
			got := ""
			if err != nil {
				got = err.(*problem).code
			}
			if got != tc.code {
				t.Fatalf("code %q (%v); want %q", got, err, tc.code)
			}
		})
	}
}

// The manifests in this repository are valid, as text.
func TestSampleManifests(t *testing.T) {
	files, _ := filepath.Glob("../../*/addon*.json")
	if len(files) < 4 {
		t.Fatalf("found only %v", files)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parse(data); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}

func TestFolder(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "demo")
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "addon.json"), []byte(good), 0o644)
	if err := checkFolder(dir); err == nil {
		t.Fatal("a folder with no program passed")
	}
	os.WriteFile(filepath.Join(dir, "bin", "demo"), nil, 0o644)
	if err := checkFolder(dir); err == nil {
		t.Fatal("a program that is not executable passed")
	}
	os.Chmod(filepath.Join(dir, "bin", "demo"), 0o755)
	if err := checkFolder(dir); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(filepath.Dir(dir), "other")
	os.Rename(dir, other)
	if err := checkFolder(other); err == nil || err.(*problem).code != "id_mismatch" {
		t.Fatalf("a folder not named by the ID: %v", err)
	}
}
