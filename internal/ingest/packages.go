// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package ingest

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/NaomiAmethyst/girlpages/internal/manpage"
	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

// PackageKind says what sort of package file path is, or "" for none.
func PackageKind(path string) string {
	base := filepath.Base(path)
	switch {
	case strings.HasSuffix(base, ".deb") || strings.HasSuffix(base, ".udeb"):
		return "deb"
	case strings.Contains(base, ".pkg.tar"):
		return "pacman"
	case strings.HasSuffix(base, ".rpm") && !strings.HasSuffix(base, ".src.rpm"):
		return "rpm"
	}
	return ""
}

var (
	archRE = regexp.MustCompile(`^(.+)-([^-]+)-([^-]+)-([^-]+)\.pkg\.tar(\.[a-z0-9]+)?$`)
	rpmRE  = regexp.MustCompile(`^(.+)-([^-]+)-([^-]+)\.([^.]+)\.rpm$`)
)

// PackageFromFile guesses a package's name, version and architecture from
// its file name, using each format's naming convention.
func PackageFromFile(path string) repo.Package {
	base := filepath.Base(path)
	var p repo.Package
	switch PackageKind(path) {
	case "deb":
		parts := strings.Split(strings.TrimSuffix(strings.TrimSuffix(base, ".deb"), ".udeb"), "_")
		if len(parts) == 3 {
			p.Name, p.Version, p.Arch = parts[0], parts[1], parts[2]
			if v, err := url.PathUnescape(p.Version); err == nil {
				p.Version = v // epochs are written %3a
			}
		}
	case "pacman":
		if m := archRE.FindStringSubmatch(base); m != nil {
			p.Name, p.Version, p.Arch = m[1], m[2]+"-"+m[3], m[4]
		}
	case "rpm":
		if m := rpmRE.FindStringSubmatch(base); m != nil {
			p.Name, p.Version, p.Arch = m[1], m[2]+"-"+m[3], m[4]
		}
	}
	return p
}

// manPattern selects what to unpack from a package.
const manPattern = "*/man/*"

// Extract unpacks the man pages from a package file into dir.
func Extract(path, dir string) error {
	switch PackageKind(path) {
	case "deb":
		return extractDeb(path, dir)
	case "pacman":
		return untar(path, dir)
	case "rpm":
		return extractRPM(path, dir)
	}
	return fmt.Errorf("%s: not a package girlrepo knows how to open", path)
}

// untar extracts man pages from a tarball of any compression GNU tar
// understands. A package without man pages isn't an error.
func untar(path, dir string) error {
	cmd := exec.Command("tar", "-x", "-f", path, "-C", dir, "--no-same-owner", "--no-same-permissions",
		"--wildcards", manPattern)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if strings.Contains(stderr.String(), "Not found in archive") {
			return nil
		}
		return fmt.Errorf("unpacking %s: %s", path, firstLine(stderr.String(), err))
	}
	return nil
}

func firstLine(s string, err error) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return err.Error()
	}
	l, _, _ := strings.Cut(s, "\n")
	return l
}

// extractDeb reads the ar archive itself, so dpkg isn't needed.
func extractDeb(path, dir string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	br := bufio.NewReader(f)
	magic := make([]byte, 8)
	if _, err := io.ReadFull(br, magic); err != nil || string(magic) != "!<arch>\n" {
		return fmt.Errorf("%s: not a .deb", path)
	}
	hdr := make([]byte, 60)
	for {
		if _, err := io.ReadFull(br, hdr); err != nil {
			if errors.Is(err, io.EOF) {
				return fmt.Errorf("%s: no data.tar inside", path)
			}
			return fmt.Errorf("%s: %w", path, err)
		}
		name := strings.TrimRight(strings.TrimSpace(string(hdr[0:16])), "/")
		size, err := strconv.ParseInt(strings.TrimSpace(string(hdr[48:58])), 10, 64)
		if err != nil {
			return fmt.Errorf("%s: bad ar header", path)
		}
		if strings.HasPrefix(name, "data.tar") {
			tmp, err := os.CreateTemp("", "girlrepo-*-"+name)
			if err != nil {
				return err
			}
			defer os.Remove(tmp.Name())
			if _, err := io.CopyN(tmp, br, size); err != nil {
				tmp.Close()
				return fmt.Errorf("%s: %w", path, err)
			}
			if err := tmp.Close(); err != nil {
				return err
			}
			return untar(tmp.Name(), dir)
		}
		if _, err := br.Discard(int(size + size%2)); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
}

// extractRPM skips the RPM lead and headers, then hands the cpio payload
// to cpio, so rpm2cpio isn't needed.
func extractRPM(path, dir string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	br := bufio.NewReader(f)
	lead := make([]byte, 96)
	if _, err := io.ReadFull(br, lead); err != nil || !bytes.HasPrefix(lead, []byte{0xed, 0xab, 0xee, 0xdb}) {
		return fmt.Errorf("%s: not an rpm", path)
	}
	for i := 0; i < 2; i++ { // signature header, then the main header
		h := make([]byte, 16)
		if _, err := io.ReadFull(br, h); err != nil || !bytes.HasPrefix(h, []byte{0x8e, 0xad, 0xe8, 0x01}) {
			return fmt.Errorf("%s: bad rpm header", path)
		}
		n := int64(binary.BigEndian.Uint32(h[8:12]))*16 + int64(binary.BigEndian.Uint32(h[12:16]))
		if i == 0 && n%8 != 0 {
			n += 8 - n%8 // the signature is padded to 8 bytes
		}
		if _, err := io.CopyN(io.Discard, br, n); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	cpio, err := manpage.Decompress(br, path)
	if err != nil {
		return err
	}
	cmd := exec.Command("cpio", "-idm", "--quiet", "--no-absolute-filenames", "-D", dir, manPattern)
	cmd.Stdin = bytes.NewReader(cpio)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("unpacking %s: %s", path, firstLine(stderr.String(), err))
	}
	return nil
}
