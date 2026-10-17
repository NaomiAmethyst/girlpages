// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package rfc

import (
	"strings"
	"testing"
)

func TestCleanFooters(t *testing.T) {
	// As RFC 768 is paginated: footers with "[page N]" in lower case, at
	// the end of the line, or on even pages at the start; and running
	// headers.
	text := "RFC 768                                                        J. Postel\n\n" +
		"Introduction\n------------\n\n" +
		"This User Datagram Protocol (UDP) is defined to make available a\n" +
		"datagram mode of packet-switched computer communication.\n\n\n" +
		"Postel                                                          [page 1]\n\f\n" +
		"                                                             28 Aug 1980\n" +
		"User Datagram Protocol                                           RFC 768\n\n" +
		"Fields\n------\n\n" +
		"Source Port is an optional field.\n\n" +
		"[page 2]                                                          Postel\n\f\n" +
		"                                                             28 Aug 1980\n" +
		"RFC 768                                           User Datagram Protocol\n" +
		"                                                                  Fields\n\n" +
		"Checksum is the 16-bit one's complement.\n\n" +
		"IP Interface\n------------\n\n" +
		"Postel                                                          [page iii]\n"
	got := Clean(text)
	// (The headers swap sides on even pages, and name the section the
	// page is in.)
	for _, gone := range []string{"[page", "28 Aug 1980", "User Datagram Protocol   ", "   User Datagram Protocol", "   Fields"} {
		if strings.Contains(got, gone) {
			t.Errorf("%q is left in:\n%s", gone, got)
		}
	}
	if strings.Count(got, "Fields") != 1 {
		t.Errorf("Fields should be there once, as a heading:\n%s", got)
	}
	for _, kept := range []string{"Source Port is an optional field.", "Checksum is the 16-bit one's complement.", "IP Interface\n----"} {
		if !strings.Contains(got, kept) {
			t.Errorf("%q went from:\n%s", kept, got)
		}
	}
	// As newer RFCs are.
	got = Clean("Text that goes on\n\nSmith                    Standards Track                    [Page 1]\n\f\n" +
		"RFC 9999                       Thing                       June 2024\n\nand on.\n\n" +
		"Smith                    Standards Track                    [Page 2]\n\f\n" +
		"RFC 9999                       Thing                       June 2024\n\nThe end.\n")
	if strings.Contains(got, "[Page") || strings.Contains(got, "RFC 9999") {
		t.Errorf("newer RFC:\n%s", got)
	}
}
