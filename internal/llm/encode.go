// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package llm

import (
	"bytes"
	"encoding/json"
)

// encode is json.Marshal, but leaving <, > and & as they are: man pages
// are full of them, and json.Marshal writes each as a six-byte escape
// where one byte does.
func encode(v any) ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// submission is a batch submission's body: the fields of head (an object,
// encoded), then "requests", the requests' items, which are encoded
// already, so each is the size it was measured as (see
// BatchAPI.RequestBytes).
func submission(head any, items [][]byte) ([]byte, error) {
	h, err := encode(head)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.Write(bytes.TrimSuffix(h, []byte("}")))
	if len(h) > 2 {
		b.WriteByte(',')
	}
	b.WriteString(`"requests":[`)
	b.Write(bytes.Join(items, []byte(",")))
	b.WriteString("]}")
	return b.Bytes(), nil
}

// itemsOf encodes requests with item, for submission.
func itemsOf(reqs []BatchRequest, item func(BatchRequest) ([]byte, error)) ([][]byte, error) {
	out := make([][]byte, len(reqs))
	for i, r := range reqs {
		b, err := item(r)
		if err != nil {
			return nil, err
		}
		out[i] = b
	}
	return out, nil
}
