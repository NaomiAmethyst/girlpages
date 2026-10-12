// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

// Package llm turns plain-text man pages into girlpages markup using an
// OpenAI-compatible API or the claude CLI.
package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// Backend is something that can answer a single prompt.
type Backend interface {
	// Name identifies the backend ("openai", "claude").
	Name() string
	// Model is the configured model, which may be empty for the default.
	Model() string
	// Complete sends one system + user prompt and returns the reply text.
	Complete(ctx context.Context, system, user string) (string, error)
}

// Options tunes a conversion.
type Options struct {
	// Kind is the kind of document (KindMan, the default, or KindRFC),
	// which has its own prompt.
	Kind              string
	ChunkSize         int
	Parallel          int // chunks converted at once
	ExtraInstructions string
	// Progress, if set, is called as chunks finish.
	Progress func(done, total int)
	// Slots, if set, is shared by conversions running at once, holding a
	// token while each request is out: so a few long pages can use all of
	// it, rather than Parallel each. Parallel still limits each page.
	Slots chan struct{}
	// Parts, if set, keeps parts as they're converted, so a conversion
	// that fails (or is stopped) part way can start again where it was.
	Parts PartCache
}

// A PartCache keeps converted parts, by PartKey.
type PartCache interface {
	Part(key string) (string, bool)
	PutPart(key, out string) error
}

// PartKey identifies a converted part: the same prompts to the same model
// get the same key.
func PartKey(b Backend, p Part) string {
	h := sha256.New()
	for _, s := range []string{b.Name(), b.Model(), p.System, p.User} {
		fmt.Fprintf(h, "%d:%s", len(s), s)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Part is one request in a conversion: a chunk of the page and the prompts
// that ask for it to be rewritten.
type Part struct {
	System, User string
	// Source is the chunk of the original page.
	Source string
	// Kind is the kind of document (KindMan, KindRFC), for checking the
	// reply.
	Kind string `json:",omitempty"`
}

// Plan works out the requests needed to convert a page. It depends only on
// its arguments, so a page planned again gives the same parts.
func Plan(title, text string, o Options) []Part {
	chunks := Split(text, o.ChunkSize)
	system := SystemPromptFor(o.Kind, o.ExtraInstructions)
	parts := make([]Part, len(chunks))
	var prev []string
	for i, c := range chunks {
		parts[i] = Part{System: system, User: UserPromptFor(o.Kind, title, i+1, len(chunks), prev, c), Source: c, Kind: o.Kind}
		prev = append(prev, Headings(c)...)
	}
	return parts
}

// Join assembles converted parts into the page.
func Join(outs []string) string {
	return strings.Join(outs, "\n\n") + "\n"
}

// Convert rewrites a whole page, splitting it into chunks if it is long.
func Convert(ctx context.Context, b Backend, title, text string, o Options) (string, error) {
	parts := Plan(title, text, o)
	chunks := make([]string, len(parts))
	for i, p := range parts {
		chunks[i] = p.Source
	}
	results := make([]string, len(chunks))
	errs := make([]error, len(chunks))
	par := o.Parallel
	if par < 1 {
		par = 1
	}
	sem := make(chan struct{}, par)
	var wg sync.WaitGroup
	var mu sync.Mutex
	done := 0
	if o.Progress != nil {
		o.Progress(0, len(chunks))
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for i := range chunks {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			}
			defer func() { <-sem }()
			key := ""
			if o.Parts != nil {
				key = PartKey(b, parts[i])
			}
			out, ok := "", false
			if key != "" {
				out, ok = o.Parts.Part(key)
			}
			if !ok {
				if o.Slots != nil {
					select {
					case o.Slots <- struct{}{}:
					case <-ctx.Done():
						errs[i] = ctx.Err()
						return
					}
				}
				var err error
				out, err = ConvertPart(ctx, b, parts[i])
				if o.Slots != nil {
					<-o.Slots
				}
				if err != nil {
					errs[i] = err
					cancel()
					return
				}
				if key != "" && len(chunks) > 1 {
					// Best effort: losing one only costs a request.
					_ = o.Parts.PutPart(key, out)
				}
			}
			results[i] = out
			if o.Progress != nil {
				mu.Lock()
				done++
				o.Progress(done, len(chunks))
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	// Report the error that caused the cancellation, not the knock-on
	// cancellations of the other chunks.
	failed := -1
	for i, err := range errs {
		if err != nil && (failed < 0 || errors.Is(errs[failed], context.Canceled) && !errors.Is(err, context.Canceled)) {
			failed = i
		}
	}
	if failed >= 0 {
		if len(chunks) > 1 {
			return "", fmt.Errorf("part %d/%d: %w", failed+1, len(chunks), errs[failed])
		}
		return "", errs[failed]
	}
	return Join(results), nil
}

// ConvertPart converts one part, retrying once if options went missing.
func ConvertPart(ctx context.Context, b Backend, p Part) (string, error) {
	reply, err := b.Complete(ctx, p.System, p.User)
	if err != nil {
		return "", err
	}
	return FinishPart(ctx, b, p, reply)
}

// FinishPart checks a part's reply (from any source, such as a batch) and,
// if it dropped options and b isn't nil, asks b once more pointing at
// exactly what went missing, keeping whichever attempt dropped less.
func FinishPart(ctx context.Context, b Backend, p Part, reply string) (string, error) {
	out := Clean(reply)
	if out == "" {
		return "", errors.New("the model returned an empty reply")
	}
	missing := Missing(p.Kind, p.Source, out)
	if len(missing) == 0 || b == nil {
		return out, nil
	}
	retry, err := b.Complete(ctx, p.System, RetryPrompt(p.User, missing))
	if err != nil {
		return out, nil
	}
	retry = Clean(retry)
	if retry != "" && len(Missing(p.Kind, p.Source, retry)) < len(missing) {
		return retry, nil
	}
	return out, nil
}

var (
	thinkRE = regexp.MustCompile(`(?s)^\s*<think>.*?</think>\s*`)
	fenceRE = regexp.MustCompile("(?s)^```[a-zA-Z]*\\n(.*)\\n```\\s*$")
)

// Clean strips wrapping the model sometimes adds despite instructions:
// reasoning blocks, a whole-reply code fence, or the input delimiters.
func Clean(s string) string {
	s = thinkRE.ReplaceAllString(s, "")
	s = strings.TrimSpace(s)
	if m := fenceRE.FindStringSubmatch(s); m != nil && !strings.Contains(m[1], "\n```") {
		s = m[1]
	}
	s = strings.TrimPrefix(s, "<<<MANPAGE")
	s = strings.TrimSuffix(s, "MANPAGE>>>")
	return strings.TrimSpace(s)
}

var optionRE = regexp.MustCompile(`(?:^|[\s\[(|,])(--?[A-Za-z][A-Za-z0-9_-]*)`)

// MissingOptions lists command-line options mentioned in the source that
// don't appear anywhere in the conversion. It's a cheap guard against the
// model "summarising" an option list.
func MissingOptions(source, out string) []string {
	seen := map[string]bool{}
	var missing []string
	for _, m := range optionRE.FindAllStringSubmatch(source, -1) {
		opt := strings.TrimRight(m[1], "-_")
		if len(opt) < 2 || opt == "--" || seen[opt] {
			continue
		}
		seen[opt] = true
		if !strings.Contains(out, opt) {
			missing = append(missing, opt)
		}
	}
	return missing
}

// Headings returns the section headings in a chunk of plain man text: the
// unindented lines, other than the page header and footer.
func Headings(text string) []string {
	var hs []string
	for _, l := range strings.Split(text, "\n") {
		if isHeading(l) {
			hs = append(hs, strings.TrimSpace(l))
		}
	}
	return hs
}

func isHeading(l string) bool {
	if l == "" || l[0] == ' ' || l[0] == '\t' {
		return false
	}
	// Header/footer lines are spread across the width with runs of spaces.
	return !strings.Contains(strings.TrimSpace(l), "   ")
}

// Split breaks plain man text into chunks of at most size bytes (when
// possible), preferring section boundaries, then paragraph boundaries, then
// line boundaries.
func Split(text string, size int) []string {
	if size <= 0 || len(text) <= size {
		return []string{text}
	}
	var units []string
	for _, sec := range splitBefore(text, isHeading) {
		if len(sec) <= size {
			units = append(units, sec)
			continue
		}
		for _, para := range splitParagraphs(sec) {
			if len(para) <= size {
				units = append(units, para)
				continue
			}
			units = append(units, splitLines(para, size)...)
		}
	}
	var chunks []string
	var cur strings.Builder
	for _, u := range units {
		if cur.Len() > 0 && cur.Len()+len(u) > size {
			chunks = append(chunks, cur.String())
			cur.Reset()
		}
		cur.WriteString(u)
	}
	if cur.Len() > 0 {
		chunks = append(chunks, cur.String())
	}
	return chunks
}

// splitBefore cuts text into pieces that each start at a line matching
// pred (except possibly the first). Pieces keep their trailing newlines.
func splitBefore(text string, pred func(string) bool) []string {
	lines := strings.SplitAfter(text, "\n")
	var out []string
	var cur strings.Builder
	for _, l := range lines {
		if cur.Len() > 0 && pred(strings.TrimRight(l, "\n")) {
			out = append(out, cur.String())
			cur.Reset()
		}
		cur.WriteString(l)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func splitParagraphs(text string) []string {
	lines := strings.SplitAfter(text, "\n")
	var out []string
	var cur strings.Builder
	for _, l := range lines {
		cur.WriteString(l)
		if strings.TrimSpace(l) == "" && cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func splitLines(text string, size int) []string {
	var out []string
	var cur strings.Builder
	for _, l := range strings.SplitAfter(text, "\n") {
		if cur.Len() > 0 && cur.Len()+len(l) > size {
			out = append(out, cur.String())
			cur.Reset()
		}
		cur.WriteString(l)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// Limit wraps a backend so that at most n requests are in flight at once,
// however many conversions are using it.
func Limit(b Backend, n int) Backend {
	return &limited{Backend: b, sem: make(chan struct{}, max(n, 1))}
}

type limited struct {
	Backend
	sem chan struct{}
}

func (l *limited) Complete(ctx context.Context, system, user string) (string, error) {
	select {
	case l.sem <- struct{}{}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	defer func() { <-l.sem }()
	return l.Backend.Complete(ctx, system, user)
}
