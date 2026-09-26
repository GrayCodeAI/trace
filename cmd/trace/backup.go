package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func backupCommand(args []string) error {
	if len(args) == 0 || (args[0] != "create" && args[0] != "verify" && args[0] != "restore") {
		return errors.New("usage: trace backup <create|verify|restore> ...")
	}
	fs := flag.NewFlagSet("backup "+args[0], flag.ContinueOnError)
	data := fs.String("data", "./data", "Trace data directory")
	out := fs.String("out", "", "backup archive path")
	force := fs.Bool("force", false, "allow restore into a non-empty directory")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	switch args[0] {
	case "create":
		if fs.NArg() != 0 || *out == "" {
			return errors.New("usage: trace backup create [-data DIR] -out FILE")
		}
		return createBackup(*data, *out)
	case "verify":
		if fs.NArg() != 0 || *out == "" {
			return errors.New("usage: trace backup verify -out FILE")
		}
		return verifyBackup(*out)
	default:
		if fs.NArg() != 0 || *out == "" {
			return errors.New("usage: trace backup restore [-data DIR] -out FILE [-force]")
		}
		return restoreBackup(*out, *data, *force)
	}
}

func createBackup(data, output string) error {
	root, err := filepath.Abs(data)
	if err != nil {
		return err
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return errors.New("backup source must be a directory")
	}
	if inside, err := pathWithin(output, root); err != nil {
		return err
	} else if inside {
		// An archive inside the tree being archived would be walked while it
		// grows, failing or embedding a partial copy of itself.
		return errors.New("backup output must be outside the data directory")
	}
	f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create backup: %w", err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tarWriter := tar.NewWriter(gz)
	err = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink in data directory: %s", rel)
		}
		h, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		h.Name = filepath.ToSlash(rel)
		if err := tarWriter.WriteHeader(h); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			in, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(tarWriter, in)
			closeErr := in.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		return nil
	})
	closeTarErr := tarWriter.Close()
	closeGzipErr := gz.Close()
	closeFileErr := f.Close()
	if err != nil {
		_ = os.Remove(output)
		return fmt.Errorf("write backup: %w", err)
	}
	if closeTarErr != nil || closeGzipErr != nil || closeFileErr != nil {
		_ = os.Remove(output)
		return errors.New("finalize backup failed")
	}
	fmt.Printf("created backup %s (%s)\n", output, time.Now().UTC().Format(time.RFC3339))
	return nil
}

func verifyBackup(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("invalid backup gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	count := 0
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("invalid backup tar: %w", err)
		}
		if err := validateArchiveName(h.Name); err != nil {
			return err
		}
		if _, err := io.Copy(io.Discard, tr); err != nil {
			return err
		}
		count++
	}
	if count == 0 {
		return errors.New("backup is empty")
	}
	fmt.Printf("verified backup %s (%d entries)\n", path, count)
	return nil
}

func restoreBackup(path, target string, force bool) error {
	if err := verifyBackup(path); err != nil {
		return err
	}
	root, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	if info, statErr := os.Stat(root); statErr == nil {
		if !info.IsDir() {
			return errors.New("restore target is not a directory")
		}
		entries, readErr := os.ReadDir(root)
		if readErr != nil {
			return readErr
		}
		if len(entries) != 0 && !force {
			return errors.New("restore target is non-empty; use -force explicitly")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, nextErr := tr.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return nextErr
		}
		if err := validateArchiveName(h.Name); err != nil {
			return err
		}
		destination := filepath.Join(root, filepath.FromSlash(h.Name))
		if h.FileInfo().IsDir() {
			if err := os.MkdirAll(destination, 0700); err != nil {
				return err
			}
			continue
		}
		if !h.FileInfo().Mode().IsRegular() {
			return fmt.Errorf("unsupported backup entry type: %s", h.Name)
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			return err
		}
		out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, tr)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func validateArchiveName(name string) error {
	clean := filepath.Clean(filepath.FromSlash(name))
	if name == "" || clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("unsafe backup entry: %q", name)
	}
	return nil
}

// pathWithin reports whether path is dir or lies below it, comparing
// symlink-resolved absolute paths. path itself need not exist yet.
func pathWithin(path, dir string) (bool, error) {
	resolvedDir, err := filepath.Abs(dir)
	if err != nil {
		return false, err
	}
	if real, err := filepath.EvalSymlinks(resolvedDir); err == nil {
		resolvedDir = real
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	parent, base := filepath.Dir(absPath), filepath.Base(absPath)
	if real, err := filepath.EvalSymlinks(parent); err == nil {
		parent = real
	}
	candidate := filepath.Join(parent, base)
	if real, err := filepath.EvalSymlinks(candidate); err == nil {
		candidate = real
	}
	rel, err := filepath.Rel(resolvedDir, candidate)
	if err != nil {
		return false, nil
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))), nil
}
