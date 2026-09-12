package main

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"syscall"
)

type InventoryFile struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

func archiveName(f *zip.File) (string, error) {
	name := strings.TrimSuffix(f.Name, "/")
	if name == "" || strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, "/") || path.Clean(name) != name {
		return "", permanent("unsafe archive path")
	}
	for _, part := range strings.Split(name, "/") {
		if part == "." || part == ".." {
			return "", permanent("unsafe archive path")
		}
	}
	if !f.Mode().IsRegular() && !f.Mode().IsDir() {
		return "", permanent("unsupported archive entry type")
	}
	if strings.HasSuffix(f.Name, "/") != f.Mode().IsDir() {
		return "", permanent("inconsistent archive entry type")
	}
	return name, nil
}

// Check the complete central directory before touching the destination.
func validateArchive(files []*zip.File, c Config, batch bool) error {
	if len(files) > c.MaxFiles {
		return permanent("archive entry count limit exceeded")
	}
	names := map[string]bool{}
	var total uint64
	for _, f := range files {
		name, err := archiveName(f)
		if err != nil {
			return err
		}
		if batch && (name == "batch.json" || strings.HasPrefix(name, "batch.json/")) {
			return permanent("reserved batch.json path")
		}
		if _, exists := names[name]; exists {
			return permanent("duplicate archive path")
		}
		names[name] = f.Mode().IsDir()
		if total > uint64(c.MaxExtracted) || f.UncompressedSize64 > uint64(c.MaxExtracted)-total {
			return permanent("declared extracted size limit exceeded")
		}
		if f.Mode().IsDir() && f.UncompressedSize64 != 0 {
			return permanent("directory entry has data")
		}
		total += f.UncompressedSize64
	}
	for name := range names {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if dir, exists := names[parent]; exists && !dir {
				return permanent("conflicting archive paths")
			}
		}
	}
	return nil
}

func safeParents(root *os.Root, name string) error {
	if name == "." {
		return nil
	}
	parts := strings.Split(name, "/")
	for i := range parts {
		p := strings.Join(parts[:i+1], "/")
		st, err := root.Lstat(p)
		if errors.Is(err, os.ErrNotExist) {
			if err = root.Mkdir(p, 0755); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !st.IsDir() {
			return permanent("destination parent is not a directory")
		}
	}
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func extractArchive(ctx context.Context, filename, destination string, c Config, overwrite bool) ([]InventoryFile, int64, error) {
	zr, err := zip.OpenReader(filename)
	if err != nil {
		return nil, 0, permanent("invalid ZIP archive")
	}
	defer zr.Close()
	if err = validateArchive(zr.File, c, !overwrite); err != nil {
		return nil, 0, err
	}
	root, err := os.OpenRoot(destination)
	if err != nil {
		return nil, 0, err
	}
	defer root.Close()
	files := make([]InventoryFile, 0)
	dirs := map[string]bool{".": true}
	var total int64
	for _, entry := range zr.File {
		if err = ctx.Err(); err != nil {
			return nil, total, err
		}
		name, _ := archiveName(entry)
		parent := path.Dir(name)
		if entry.Mode().IsDir() {
			parent = name
		}
		if err = safeParents(root, parent); err != nil {
			return nil, total, err
		}
		for p := parent; p != "."; p = path.Dir(p) {
			dirs[p] = true
		}
		if entry.Mode().IsDir() {
			continue
		}
		flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
		if overwrite {
			st, statErr := root.Lstat(name)
			if statErr == nil && !st.Mode().IsRegular() {
				return nil, total, permanent("destination is not a regular file")
			}
			if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
				return nil, total, statErr
			}
			// Open before truncation so hard links and special files can be checked.
			flags = os.O_WRONLY | os.O_CREATE | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
		}
		dst, err := root.OpenFile(name, flags, 0644)
		if err != nil {
			return nil, total, err
		}
		st, err := dst.Stat()
		if err != nil || !st.Mode().IsRegular() {
			dst.Close()
			return nil, total, permanent("destination is not an unlinked regular file")
		}
		stat, statOK := st.Sys().(*syscall.Stat_t)
		if !statOK || stat.Nlink != 1 {
			dst.Close()
			return nil, total, permanent("destination is not an unlinked regular file")
		}
		if overwrite {
			err = dst.Truncate(0)
		}
		if err == nil {
			err = dst.Chmod(0644)
		}
		if err != nil {
			dst.Close()
			return nil, total, err
		}
		src, err := entry.Open()
		if err != nil {
			dst.Close()
			return nil, total, permanent("invalid ZIP entry")
		}
		h := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(dst, h), io.LimitReader(contextReader{ctx, src}, c.MaxExtracted-total+1))
		srcErr := src.Close()
		syncErr := dst.Sync()
		closeErr := dst.Close()
		if ctx.Err() != nil {
			return nil, total, ctx.Err()
		}
		if n > c.MaxExtracted-total || uint64(n) != entry.UncompressedSize64 {
			return nil, total, permanent("extracted entry size mismatch or limit exceeded")
		}
		if copyErr != nil || srcErr != nil {
			return nil, total, permanent("invalid or truncated ZIP contents")
		}
		if syncErr != nil || closeErr != nil {
			return nil, total, errors.New("cannot persist extracted file")
		}
		total += n
		files = append(files, InventoryFile{name, n, fmt.Sprintf("%x", h.Sum(nil))})
	}
	// Sync children before their parents, including implicit archive directories.
	ordered := make([]string, 0, len(dirs))
	for p := range dirs {
		ordered = append(ordered, p)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ordered)))
	for _, p := range ordered {
		d, err := root.Open(p)
		if err != nil {
			return nil, total, err
		}
		err = d.Sync()
		closeErr := d.Close()
		if err != nil {
			return nil, total, err
		}
		if closeErr != nil {
			return nil, total, closeErr
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, total, nil
}
