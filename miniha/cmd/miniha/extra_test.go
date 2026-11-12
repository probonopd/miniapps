/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package main

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func tarNames(t *testing.T, file string) []string {
	t.Helper()
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return names
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
}

func fixtureDir(t *testing.T) string {
	dir := t.TempDir()
	writeFile(t, dir, "configuration.yaml", "homeassistant:\n  name: Test\n", 0o644)
	writeFile(t, dir, "secrets.yaml", "k: v\n", 0o600)
	writeFile(t, dir, "blueprints/a/b.yaml", "x: 1\n", 0o644)
	writeFile(t, dir, "history.db-wal", "wal", 0o644)
	writeFile(t, dir, "auth.json.tmp", "partial", 0o644)
	writeFile(t, dir, "unrelated.txt", "ignore", 0o644)
	return dir
}

func TestBackupRestoreRoundTrip(t *testing.T) {
	src := fixtureDir(t)
	file := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := extraCommand("backup", src, []string{file}); err != nil {
		t.Fatal(err)
	}
	names := strings.Join(tarNames(t, file), " ")
	for _, want := range []string{"configuration.yaml", "secrets.yaml", "blueprints/a/b.yaml", "history.db-wal"} {
		if !strings.Contains(names, want) {
			t.Errorf("%s missing from %s", want, names)
		}
	}
	for _, bad := range []string{".tmp", "unrelated.txt"} {
		if strings.Contains(names, bad) {
			t.Errorf("%s must not be archived: %s", bad, names)
		}
	}
	dst := t.TempDir()
	if err := extraCommand("restore", dst, []string{file}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"configuration.yaml", "secrets.yaml", "blueprints/a/b.yaml", "history.db-wal"} {
		if readFile(t, src, n) != readFile(t, dst, n) {
			t.Errorf("%s differs after restore", n)
		}
	}
	st, err := os.Stat(filepath.Join(dst, "secrets.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("secrets.yaml mode %v, want 0600", st.Mode().Perm())
	}
}

func TestBackupRefusesOverwrite(t *testing.T) {
	src := fixtureDir(t)
	file := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := extraCommand("backup", src, []string{file}); err != nil {
		t.Fatal(err)
	}
	if err := extraCommand("backup", src, []string{file}); err == nil {
		t.Fatal("second backup without --force must fail")
	}
	if err := extraCommand("backup", src, []string{file, "--force"}); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreRefusesExistingConfig(t *testing.T) {
	src := fixtureDir(t)
	file := filepath.Join(t.TempDir(), "b.tar.gz")
	if err := extraCommand("backup", src, []string{file}); err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	writeFile(t, dst, "configuration.yaml", "old\n", 0o644)
	if err := extraCommand("restore", dst, []string{file}); err == nil {
		t.Fatal("restore over existing configuration.yaml must fail")
	}
	if readFile(t, dst, "configuration.yaml") != "old\n" {
		t.Fatal("refused restore modified the data directory")
	}
	if err := extraCommand("restore", dst, []string{file, "--force"}); err != nil {
		t.Fatal(err)
	}
	if readFile(t, dst, "configuration.yaml") != readFile(t, src, "configuration.yaml") {
		t.Fatal("forced restore did not replace configuration.yaml")
	}
}

func TestRestoreRejectsPathTraversal(t *testing.T) {
	for _, name := range []string{"../x", "a/../../x", "/abs"} {
		root := t.TempDir()
		dst := filepath.Join(root, "data")
		file := filepath.Join(root, "evil.tar.gz")
		f, err := os.Create(file)
		if err != nil {
			t.Fatal(err)
		}
		gz := gzip.NewWriter(f)
		tw := tar.NewWriter(gz)
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		tw.Close()
		gz.Close()
		f.Close()
		if err := extraCommand("restore", dst, []string{file}); err == nil {
			t.Errorf("%q: restore must fail", name)
		}
		if _, err := os.Stat(filepath.Join(root, "x")); err == nil {
			t.Errorf("%q: file escaped the data directory", name)
		}
	}
}

func TestUnknownArguments(t *testing.T) {
	dir := t.TempDir()
	if err := extraCommand("backup", dir, []string{"f.tar.gz", "--bogus"}); err == nil {
		t.Error("backup: unknown option must fail")
	}
	if err := extraCommand("restore", dir, []string{"a", "b"}); err == nil {
		t.Error("restore: extra argument must fail")
	}
	if err := extraCommand("check-config", dir, []string{"x"}); err == nil {
		t.Error("check-config: argument must fail")
	}
	if err := extraCommand("backup", dir, nil); err == nil {
		t.Error("backup: missing file must fail")
	}
}

func TestCheckConfigValid(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "configuration.yaml", "homeassistant:\n  name: Test\n", 0o644)
	writeFile(t, dir, "ui-lovelace.yaml", "views:\n  - title: Home\n", 0o644)
	if err := extraCommand("check-config", dir, nil); err != nil {
		t.Fatal(err)
	}
}

func TestCheckConfigInvalid(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "configuration.yaml", "homeassistant:\n  name: Test\n", 0o644)
	writeFile(t, dir, "automations.yaml", "this: [is: not\n", 0o644)
	if err := extraCommand("check-config", dir, nil); err == nil {
		t.Fatal("invalid automations.yaml must fail")
	}
}

func TestCheckConfigInvalidLovelace(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "configuration.yaml", "homeassistant:\n  name: Test\n", 0o644)
	writeFile(t, dir, "ui-lovelace.yaml", "title: no views\n", 0o644)
	if err := extraCommand("check-config", dir, nil); err == nil {
		t.Fatal("ui-lovelace.yaml without views must fail")
	}
}

func TestCheckConfigMissingConfiguration(t *testing.T) {
	if err := extraCommand("check-config", t.TempDir(), nil); err == nil {
		t.Fatal("missing configuration.yaml must fail")
	}
}
