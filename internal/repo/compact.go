// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package repo

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// Indexes are made to be small: lists of records are tables, their fields
// named once, and hashes in them are written in basE91.

// b91Alphabet is basE91's, with ' in place of " so that encoded strings
// need no escaping in JSON.
const b91Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789!#$%&()*+,./:;<=>?@[]^_`{|}~'"

var b91Decode = func() [256]int {
	var d [256]int
	for i := range d {
		d[i] = -1
	}
	for i := 0; i < len(b91Alphabet); i++ {
		d[b91Alphabet[i]] = i
	}
	return d
}()

// Encode91 encodes bytes in basE91 (JSON-safe).
func Encode91(b []byte) string {
	var out strings.Builder
	var queue, nbits uint
	for _, c := range b {
		queue |= uint(c) << nbits
		nbits += 8
		if nbits > 13 {
			v := queue & 8191
			if v > 88 {
				queue >>= 13
				nbits -= 13
			} else {
				v = queue & 16383
				queue >>= 14
				nbits -= 14
			}
			out.WriteByte(b91Alphabet[v%91])
			out.WriteByte(b91Alphabet[v/91])
		}
	}
	if nbits > 0 {
		out.WriteByte(b91Alphabet[queue%91])
		if nbits > 7 || queue > 90 {
			out.WriteByte(b91Alphabet[queue/91])
		}
	}
	return out.String()
}

// Decode91 decodes basE91 (JSON-safe).
func Decode91(s string) ([]byte, error) {
	var out []byte
	var queue, nbits uint
	v := -1
	for i := 0; i < len(s); i++ {
		c := b91Decode[s[i]]
		if c < 0 {
			return nil, fmt.Errorf("%q isn't basE91", s)
		}
		if v < 0 {
			v = c
			continue
		}
		v += c * 91
		queue |= uint(v) << nbits
		if v&8191 > 88 {
			nbits += 13
		} else {
			nbits += 14
		}
		for nbits > 7 {
			out = append(out, byte(queue))
			queue >>= 8
			nbits -= 8
		}
		v = -1
	}
	if v >= 0 {
		out = append(out, byte(queue|uint(v)<<nbits))
	}
	return out, nil
}

// hash91 writes a hex hash in basE91; "" stays "".
func hash91(h string) string {
	if h == "" {
		return ""
	}
	b, err := hex.DecodeString(h)
	if err != nil {
		return h
	}
	return Encode91(b)
}

// unhash91 reads a hash written by hash91 back as hex.
func unhash91(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	b, err := Decode91(s)
	if err != nil {
		return "", err
	}
	if len(b) != 32 {
		return "", fmt.Errorf("%q isn't a SHA-256 hash", s)
	}
	return hex.EncodeToString(b), nil
}

// Table is records stored by column: Keys names their fields, and Data
// holds each record's values in turn, len(Keys) to a record. Readers find
// fields by name and ignore ones they don't know.
type Table struct {
	Keys []string          `json:"keys"`
	Data []json.RawMessage `json:"data"`
}

// tableWriter builds a Table.
type tableWriter struct {
	keys []string
	data []any
}

func newTable(keys ...string) *tableWriter { return &tableWriter{keys: keys} }

func (t *tableWriter) add(values ...any) {
	if len(values) != len(t.keys) {
		panic("table: wrong number of values")
	}
	t.data = append(t.data, values...)
}

func (t *tableWriter) MarshalJSON() ([]byte, error) {
	data := t.data
	if data == nil {
		data = []any{}
	}
	return marshal(struct {
		Keys []string `json:"keys"`
		Data []any    `json:"data"`
	}{t.keys, data})
}

// rows calls fn with each record of a table as a function from field
// names to raw values (nil if it hasn't one).
func (t *Table) rows(fn func(get func(key string) json.RawMessage) error) error {
	n := len(t.Keys)
	if n == 0 {
		return nil
	}
	if len(t.Data)%n != 0 {
		return fmt.Errorf("table has %d values for %d fields", len(t.Data), n)
	}
	col := map[string]int{}
	for i, k := range t.Keys {
		col[k] = i
	}
	for r := 0; r < len(t.Data); r += n {
		row := t.Data[r : r+n]
		get := func(key string) json.RawMessage {
			if i, ok := col[key]; ok {
				return row[i]
			}
			return nil
		}
		if err := fn(get); err != nil {
			return err
		}
	}
	return nil
}

// field decodes a raw value into v, leaving it as it is if there's none.
func field(raw json.RawMessage, v any) error {
	if raw == nil || string(raw) == "null" {
		return nil
	}
	return json.Unmarshal(raw, v)
}

// marshal encodes as Encode does, without the trailing newline.
func marshal(v any) ([]byte, error) {
	b, err := Encode(v)
	if err != nil {
		return nil, err
	}
	return []byte(strings.TrimSuffix(string(b), "\n")), nil
}
