package prune

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jailops.net/pkgops/internal/index"
)

const abi = "FreeBSD:15:amd64"

func idx(now time.Time, vers ...string) *index.Index {
	ix := index.New()
	for i, v := range vers {
		ix.Packages = append(ix.Packages, index.Entry{ABI: abi, Name: "hello", Version: v, File: "f/" + v, SHA256: strings.Repeat("a", 64),
			Size: 100, Built: now.Add(-time.Duration(len(vers)-i) * 48 * time.Hour)})
	}
	return ix
}

func TestPlanKeepOlderThanAndExact(t *testing.T) {
	now := time.Now()
	ix := idx(now, "1.0", "1.1", "1.2", "1.3")
	got, err := Plan(ix, New(), Selector{Keep: 2, Now: now})
	if err != nil || len(got) != 2 || got[0].Version != "1.0" || got[1].Version != "1.1" {
		t.Fatalf("keep 2: %v %v", got, err)
	}
	got, err = Plan(ix, New(), Selector{OlderThan: 5 * 24 * time.Hour, Now: now})
	if err != nil || len(got) != 2 { // built 8, 6, 4, 2 days ago
		t.Fatalf("older-than: %v %v", got, err)
	}
	got, err = Plan(ix, New(), Selector{Name: "hello", Version: "1.1", Now: now})
	if err != nil || len(got) != 1 || got[0].Version != "1.1" {
		t.Fatalf("exact: %v %v", got, err)
	}
	// already listed entries are not repeated
	m := New()
	m.Merge(got)
	again, _ := Plan(ix, m, Selector{Name: "hello", Version: "1.1", Now: now})
	if len(again) != 0 {
		t.Errorf("repeated: %v", again)
	}
	if _, err := Plan(ix, New(), Selector{Now: now}); err == nil {
		t.Error("an empty selection must be refused")
	}
}

func TestNewestIsProtectedUnlessForced(t *testing.T) {
	now := time.Now()
	ix := idx(now, "1.0", "1.1")
	if _, err := Plan(ix, New(), Selector{Name: "hello", Version: "1.1", Now: now}); !errors.Is(err, ErrNewest) {
		t.Fatalf("exact newest: %v", err)
	}
	got, err := Plan(ix, New(), Selector{Name: "hello", Version: "1.1", Force: true, Now: now})
	if err != nil || len(got) != 1 || !got[0].Force {
		t.Fatalf("forced: %v %v", got, err)
	}
	// keep never selects the newest, even with keep 1 on a single version
	if got, _ := Plan(idx(now, "1.0"), New(), Selector{Keep: 1, Now: now}); len(got) != 0 {
		t.Errorf("keep 1 of 1: %v", got)
	}
	m := &Manifest{Schema: 1, Entries: []Entry{{ABI: abi, Name: "hello", Version: "1.1"}}}
	if err := m.Check(ix); !errors.Is(err, ErrNewest) {
		t.Errorf("check: %v", err)
	}
	m.Entries[0].Force = true
	if err := m.Check(ix); err != nil {
		t.Errorf("forced check: %v", err)
	}
	// entries that are already gone are fine (idempotent apply)
	m = &Manifest{Schema: 1, Entries: []Entry{{ABI: abi, Name: "hello", Version: "0.1"}}}
	if err := m.Check(ix); err != nil {
		t.Errorf("gone entry: %v", err)
	}
}

func TestManifestRoundTripAndValidation(t *testing.T) {
	file := filepath.Join(t.TempDir(), "prune.yaml")
	m, err := Load(file)
	if err != nil || len(m.Entries) != 0 {
		t.Fatalf("missing file is an empty manifest: %v %v", m, err)
	}
	m.Merge([]Entry{{ABI: abi, Name: "b", Version: "2", Reason: "old"}, {ABI: abi, Name: "a", Version: "1"}})
	if err := m.Save(file); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(file)
	if !strings.HasPrefix(string(raw), "# Prune manifest") || strings.Index(string(raw), "name: a") > strings.Index(string(raw), "name: b") {
		t.Errorf("header or order:\n%s", raw)
	}
	back, err := Load(file)
	if err != nil || len(back.Entries) != 2 {
		t.Fatalf("%v %v", back, err)
	}
	for _, bad := range []string{
		"entries: [{abi: FreeBSD:15:amd64, name: '../x', version: '1'}]",
		"entries: [{abi: nonsense, name: a, version: '1'}]",
		"entries: [{abi: FreeBSD:15:amd64, name: a, version: ''}]",
		"entries: [{abi: FreeBSD:15:amd64, name: a, version: '1', colour: red}]",
		"entries: [{abi: FreeBSD:15:amd64, name: a, version: '1'}, {abi: FreeBSD:15:amd64, name: a, version: '1'}]",
		"schema: 9",
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if !back.Remove(abi, "a", "1") || back.Remove(abi, "a", "1") {
		t.Error("Remove")
	}
}
