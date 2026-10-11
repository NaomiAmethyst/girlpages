// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package crawl

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"github.com/NaomiAmethyst/girlpages/internal/version"
	"io"
	"strings"
)

// RPM is an rpm-md (yum/dnf) repository: the directory holding repodata/,
// such as https://dl.fedoraproject.org/pub/fedora/linux/releases/42/Everything/x86_64/os.
type RPM struct {
	URL string
	// All fetches every package if there's no filelists index.
	All bool
}

type repomd struct {
	Data []struct {
		Type     string `xml:"type,attr"`
		Location struct {
			Href string `xml:"href,attr"`
		} `xml:"location"`
	} `xml:"data"`
}

func (r *RPM) Index(ctx context.Context, f *Fetcher) (*Index, error) {
	rd, _, err := f.OpenFirst(ctx, join(r.URL, "repodata/repomd.xml"))
	if err != nil {
		return nil, fmt.Errorf("reading repomd.xml: %w", err)
	}
	var md repomd
	err = xml.NewDecoder(rd).Decode(&md)
	rd.Close()
	if err != nil {
		return nil, fmt.Errorf("repomd.xml: %w", err)
	}
	locs := map[string]string{}
	for _, d := range md.Data {
		locs[d.Type] = d.Location.Href
	}
	if locs["primary"] == "" {
		return nil, errors.New("repomd.xml lists no primary index")
	}

	// filelists says which packages have man pages; it's keyed by pkgid,
	// the package checksum, as primary is.
	withMan := map[string]bool{}
	haveLists := locs["filelists"] != ""
	if haveLists {
		err := r.each(ctx, f, locs["filelists"], "package", func(d *xml.Decoder, start xml.StartElement) error {
			var p struct {
				PkgID string   `xml:"pkgid,attr"`
				Files []string `xml:"file"`
			}
			if err := d.DecodeElement(&p, &start); err != nil {
				return err
			}
			for _, file := range p.Files {
				if IsManPath(file) {
					withMan[p.PkgID] = true
					break
				}
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("filelists: %w", err)
		}
	} else if !r.All {
		return nil, errors.New("this repository has no filelists index to say which packages have man pages; --all downloads every package instead")
	}

	idx := &Index{Compare: version.RPM}
	err = r.each(ctx, f, locs["primary"], "package", func(d *xml.Decoder, start xml.StartElement) error {
		var p struct {
			Name    string `xml:"name"`
			Arch    string `xml:"arch"`
			Version struct {
				Epoch string `xml:"epoch,attr"`
				Ver   string `xml:"ver,attr"`
				Rel   string `xml:"rel,attr"`
			} `xml:"version"`
			Checksum struct {
				Type  string `xml:"type,attr"`
				Value string `xml:",chardata"`
			} `xml:"checksum"`
			Size struct {
				Package int64 `xml:"package,attr"`
			} `xml:"size"`
			Location struct {
				Href    string `xml:"href,attr"`
				XMLBase string `xml:"base,attr"`
			} `xml:"location"`
		}
		if err := d.DecodeElement(&p, &start); err != nil {
			return err
		}
		if p.Arch == "src" || p.Arch == "nosrc" {
			return nil
		}
		idx.Total++
		sum := strings.TrimSpace(p.Checksum.Value)
		if haveLists && !withMan[sum] {
			return nil
		}
		v := p.Version.Ver + "-" + p.Version.Rel
		if p.Version.Epoch != "" && p.Version.Epoch != "0" {
			v = p.Version.Epoch + ":" + v
		}
		base := r.URL
		if p.Location.XMLBase != "" {
			base = p.Location.XMLBase
		}
		pk := Package{Name: p.Name, Version: v, Arch: p.Arch, URL: join(base, p.Location.Href), Size: p.Size.Package}
		if p.Checksum.Type == "sha256" {
			pk.SHA256 = sum
		}
		idx.Packages = append(idx.Packages, pk)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("primary: %w", err)
	}
	return idx, nil
}

// each streams an XML index, calling fn for each element named name.
func (r *RPM) each(ctx context.Context, f *Fetcher, href, name string, fn func(*xml.Decoder, xml.StartElement) error) error {
	rd, _, err := f.openIndex(ctx, join(r.URL, href))
	if err != nil {
		return err
	}
	defer rd.Close()
	d := xml.NewDecoder(rd)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		tok, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == name {
			if err := fn(d, se); err != nil {
				return err
			}
		}
	}
}
