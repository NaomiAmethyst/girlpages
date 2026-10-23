// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/NaomiAmethyst/girlpages/internal/pager"
	"github.com/NaomiAmethyst/girlpages/internal/render"
	"github.com/NaomiAmethyst/girlpages/internal/repo"
)

// Exit statuses, as man(1) has them.
const (
	ExitOK       = 0
	ExitUsage    = 1
	ExitFailed   = 2
	ExitNotFound = 16
)

// Reader reads pages from a repository on the command line, as man does.
type Reader struct {
	Repo *Repo
	Options
	// Prog is the command's name, for messages.
	Prog string
	// Width and Color are how pages are rendered.
	Width int
	Color render.ColorMode
	// Footer goes at the bottom left of pages ("girlpages 1.0").
	Footer string
	// Raw prints the stored text of pages instead of rendering them, and
	// Markdown prints them as Markdown (originals in a code block).
	Raw, Markdown bool
	// Page pages what's read with Pager (as pager.Command takes it: ""
	// for $GIRLPAGER, $PAGER or less); otherwise it's written to Stdout.
	Page           bool
	Pager          string
	Stdout, Stderr io.Writer
}

func (rd *Reader) fail(err error, what string) int {
	if errors.Is(err, repo.ErrNotFound) {
		fmt.Fprintf(rd.Stderr, "%s: %v 🤷\n", rd.Prog, err)
		return ExitNotFound
	}
	fmt.Fprintf(rd.Stderr, "%s: couldn't get %s from the repo 😭 %v\n", rd.Prog, what, err)
	return ExitFailed
}

// Read shows the pages args ask for ("[section] page ...").
func (rd *Reader) Read(ctx context.Context, args []string) int {
	return rd.ReadRequests(ctx, Requests(args))
}

// ReadRequests shows the pages asked for.
func (rd *Reader) ReadRequests(ctx context.Context, qs []Request) int {
	plain := rd.Raw || rd.Markdown // as they are, without a pager
	status := ExitOK
	var out strings.Builder
	var first string
	for _, q := range qs {
		pages, err := rd.Repo.Find(ctx, q, rd.Options)
		if err != nil {
			if ctx.Err() != nil {
				return ExitFailed
			}
			status = max(status, rd.fail(err, q.String()))
			continue
		}
		for _, p := range pages {
			if first == "" {
				first = p.Title()
			} else if !rd.Raw {
				out.WriteString("\n")
			}
			out.WriteString(rd.text(&p))
		}
	}
	if first == "" {
		return status
	}
	if rd.Page && !plain {
		if err := pager.Show(pager.Command(rd.Pager), out.String(), "💅 "+rd.Prog+" page "+first); err != nil {
			fmt.Fprintf(rd.Stderr, "%s: %v\n", rd.Prog, err)
			return ExitFailed
		}
		return status
	}
	io.WriteString(rd.Stdout, out.String())
	return status
}

// text is a page as it's shown.
func (rd *Reader) text(p *Page) string {
	if rd.Raw {
		return render.StripControls(p.Variant.Text())
	}
	if rd.Markdown {
		return rd.markdown(p)
	}
	footer := rd.Footer
	if about := p.About(); about != "" {
		footer += " · " + about
	}
	var note string
	if p.Fallback {
		note = "💭 she hasn't gotten to this one yet, bestie, so here's the original"
		if g := p.Girly(); g != "" {
			note += fmt.Sprintf(" (--page-version %s is girlified)", g)
		}
		note += "\n\n"
	}
	if p.Variant.Syntax != repo.SyntaxMarkup {
		if about := p.About(); about != "" {
			note += "📌 " + p.Title() + ", " + about + "\n\n"
		}
		return note + p.Text(render.Options{})
	}
	return note + p.Text(render.Options{Width: rd.Width, Color: rd.Color, Footer: footer})
}

// markdown is a page as Markdown: girlpages markup without its tags, or
// an original, as man prints it, in a code block under its title.
func (rd *Reader) markdown(p *Page) string {
	var note string
	if p.Fallback {
		note = "> 💭 she hasn't gotten to this one yet, bestie, so here's the original"
		if g := p.Girly(); g != "" {
			note += fmt.Sprintf(" (--page-version %s is girlified)", g)
		}
		note += "\n\n"
	}
	if about := p.About(); about != "" {
		note += "> 📌 " + p.Title() + ", " + about + "\n\n"
	}
	text := render.StripControls(p.Variant.Text())
	if p.Variant.Syntax == repo.SyntaxMarkup {
		return note + render.Markdown(text)
	}
	fence := "```"
	for strings.Contains(text, fence) {
		fence += "`"
	}
	return "# " + p.Title() + "\n\n" + note + fence + "\n" + strings.TrimRight(text, "\n") + "\n" + fence + "\n"
}

// Versions lists the versions of the pages args ask for.
func (rd *Reader) Versions(ctx context.Context, args []string) int {
	status := ExitOK
	shown := 0
	for _, q := range Requests(args) {
		ls, err := rd.Repo.Listings(ctx, q, rd.Options)
		if err != nil {
			if ctx.Err() != nil {
				return ExitFailed
			}
			status = max(status, rd.fail(err, q.String()))
			continue
		}
		for _, l := range ls {
			if shown > 0 {
				fmt.Fprintln(rd.Stdout)
			}
			shown++
			if err := WriteVersions(rd.Stdout, l); err != nil {
				return ExitFailed
			}
		}
	}
	return status
}

// Apropos searches for pages by what they're about, as man -k does: each
// argument on its own, unless there's one (which can be several words).
func (rd *Reader) Apropos(ctx context.Context, args []string, limit int) int {
	found := false
	for _, a := range args {
		hits, err := rd.Repo.Search(ctx, a, rd.Sections, limit)
		if err != nil {
			return rd.fail(err, "search results")
		}
		if len(hits) == 0 {
			fmt.Fprintf(rd.Stderr, "%s: nothing appropriate for %q 🤷\n", rd.Prog, a)
			continue
		}
		found = true
		if err := WriteHits(rd.Stdout, hits); err != nil {
			return ExitFailed
		}
	}
	if !found {
		return ExitNotFound
	}
	return ExitOK
}

// Whatis gives the one-line descriptions of pages by name, as man -f
// does.
func (rd *Reader) Whatis(ctx context.Context, args []string) int {
	status := ExitOK
	for _, q := range Requests(args) {
		hits, err := rd.Repo.Whatis(ctx, q, rd.Sections)
		if err != nil {
			return rd.fail(err, q.String())
		}
		if len(hits) == 0 {
			fmt.Fprintf(rd.Stderr, "%s: nothing appropriate 🤷\n", q)
			status = ExitNotFound
			continue
		}
		if err := WriteHits(rd.Stdout, hits); err != nil {
			return ExitFailed
		}
	}
	return status
}
