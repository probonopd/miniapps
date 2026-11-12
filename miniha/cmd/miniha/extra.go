/*
 * Copyright (c) 2026 Simon Peter
 *
 * SPDX-License-Identifier: MIT
 */

package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/probonopd/miniha/internal/automation"
	"github.com/probonopd/miniha/internal/store"
)

// backupEntries are the data directory members a backup carries. An explicit
// list keeps stray files (editor backups, core dumps) out of the archive and
// makes a restore predictable.
var backupEntries = []string{
	"configuration.yaml",
	"secrets.yaml",
	"automations.yaml",
	"scripts.yaml",
	"scenes.yaml",
	"ui-lovelace.yaml",
	"blueprints",
	"registry.json",
	"auth.json",
	"helpers_state.json",
	"automation_state.json",
	"broker",
	"history.db",
	"history.db-wal",
	"history.db-shm",
}

const forceFlag = "--force"

// extraCommand runs backup, restore and check-config.
func extraCommand(cmd, dir string, args []string) error {
	switch cmd {
	case "backup":
		file, force, err := fileAndForce(cmd, args)
		if err != nil {
			return err
		}
		return backup(dir, file, force)
	case "restore":
		file, force, err := fileAndForce(cmd, args)
		if err != nil {
			return err
		}
		return restore(dir, file, force)
	case "check-config":
		if len(args) > 0 {
			return fmt.Errorf("check-config: unexpected argument %q", args[0])
		}
		return checkConfig(dir)
	}
	return fmt.Errorf("%s is not available in this build", cmd)
}

// fileAndForce parses "FILE [--force]" in either order.
func fileAndForce(cmd string, args []string) (string, bool, error) {
	var file string
	force := false
	for _, a := range args {
		switch {
		case a == forceFlag:
			force = true
		case strings.HasPrefix(a, "-"):
			return "", false, fmt.Errorf("%s: unknown option %q", cmd, a)
		case file != "":
			return "", false, fmt.Errorf("%s: unexpected argument %q", cmd, a)
		default:
			file = a
		}
	}
	if file == "" {
		return "", false, fmt.Errorf("usage: miniha backup|restore FILE.tar.gz [%s]", forceFlag)
	}
	return file, force, nil
}

// backup writes the data directory to a gzip tarball. The archive is built
// under a temporary name and renamed at the end so that a failed run never
// leaves a truncated backup behind.
func backup(dir, file string, force bool) (err error) {
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("data directory: %w", err)
	}
	if _, err := os.Lstat(file); err == nil {
		if !force {
			return fmt.Errorf("%s exists; use %s to overwrite", file, forceFlag)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	tmp := file + ".tmp"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			out.Close()
			os.Remove(tmp)
		}
	}()
	outInfo, err := out.Stat()
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	count := 0
	for _, name := range backupEntries {
		n, err := addEntry(tw, dir, name, outInfo)
		if err != nil {
			return err
		}
		count += n
	}
	if count == 0 {
		return fmt.Errorf("nothing to back up in %s", dir)
	}
	if err = tw.Close(); err != nil {
		return err
	}
	if err = gz.Close(); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, file); err != nil {
		return err
	}
	st, err := os.Stat(file)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "backup %s: %d files, %d bytes\n", file, count, st.Size())
	return nil
}

// addEntry adds one named file or directory tree and returns the number of
// regular files written. A missing entry is fine: not every installation has
// a broker or a history database.
func addEntry(tw *tar.Writer, dir, name string, skip os.FileInfo) (int, error) {
	root := filepath.Join(dir, name)
	if _, err := os.Lstat(root); errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	count := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".tmp") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !d.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("%s: only regular files and directories can be backed up", p)
		}
		if !d.IsDir() && os.SameFile(info, skip) {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if d.IsDir() {
			hdr.Name += "/"
		}
		// Owner names are meaningless on the restoring machine.
		hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 0, 0, "", ""
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		// A growing file (the history WAL) would make the copy longer than the
		// header size; limiting to the header size keeps the archive valid.
		if _, err := io.CopyN(tw, f, hdr.Size); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		count++
		return nil
	})
	return count, err
}

// restore extracts a backup into dir. miniha must be stopped first: restore
// does not check for a running instance, because there is no pid file to
// check and a half-written database under a live process is not detectable.
func restore(dir, file string, force bool) error {
	if _, err := os.Lstat(filepath.Join(dir, "configuration.yaml")); err == nil {
		if !force {
			return fmt.Errorf("%s already contains configuration.yaml; use %s to overwrite", dir, forceFlag)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	in, err := os.Open(file)
	if err != nil {
		return err
	}
	defer in.Close()
	gz, err := gzip.NewReader(in)
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	defer gz.Close()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	count := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
		rel, err := safeName(hdr.Name)
		if err != nil {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(rel))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			if err := os.Chmod(target, hdr.FileInfo().Mode().Perm()); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := extractFile(tr, target, hdr.FileInfo().Mode().Perm()); err != nil {
				return err
			}
			fmt.Fprintln(os.Stderr, "restored", rel)
			count++
		default:
			return fmt.Errorf("%s: unsupported entry type for %q", file, hdr.Name)
		}
	}
	if count == 0 {
		return fmt.Errorf("%s contains no files", file)
	}
	fmt.Fprintf(os.Stderr, "restored %d files into %s\n", count, dir)
	return nil
}

// safeName rejects entries that could write outside the data directory.
func safeName(name string) (string, error) {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return "", fmt.Errorf("unsafe path %q in archive", name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", fmt.Errorf("unsafe path %q in archive", name)
		}
	}
	clean := path.Clean(name)
	if clean == "." {
		return "", fmt.Errorf("unsafe path %q in archive", name)
	}
	return clean, nil
}

func extractFile(r io.Reader, target string, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// The open mode is filtered by the umask; secrets must come back exactly.
	return os.Chmod(target, perm)
}

// checkConfig validates everything miniha would read at startup and reports
// every problem, not just the first.
func checkConfig(dir string) error {
	var errs []string
	fail := func(file string, err error) {
		errs = append(errs, fmt.Sprintf("%s: %v", file, err))
	}
	if _, err := store.LoadConfig(filepath.Join(dir, "configuration.yaml")); err != nil {
		fail("configuration.yaml", err)
	}
	for _, name := range automation.Files {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			fail(name, err)
			continue
		}
		if err := automation.ValidateWithBlueprints(dir, name, data); err != nil {
			fail(name, err)
		}
	}
	if err := checkLovelace(filepath.Join(dir, "ui-lovelace.yaml")); err != nil {
		fail("ui-lovelace.yaml", err)
	}
	if len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintln(os.Stderr, e)
		}
		return fmt.Errorf("%d configuration error(s)", len(errs))
	}
	fmt.Println("configuration valid")
	return nil
}

// checkLovelace requires a views list; the dashboard cannot render without it.
func checkLovelace(p string) error {
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var doc struct {
		Views []any `yaml:"views"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	if doc.Views == nil {
		return errors.New("missing views list")
	}
	return nil
}
