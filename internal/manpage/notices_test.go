// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>

package manpage

import (
	"reflect"
	"testing"
)

func TestFindNotices(t *testing.T) {
	for _, c := range []struct {
		src  string
		want Notices
	}{
		{`.\" This manpage is Copyright (C) 1992 Drew Eckhardt;
.\" and Copyright (C) 1993 Michael Haardt, Ian Jackson.
.\"
.\" SPDX-License-Identifier: Linux-man-pages-copyleft
.\"
.\" Modified 1993-07-21 by Rik Faith <faith@cs.unc.edu>
.TH OPEN 2
.SH NAME
open \- Copyright 1999 isn't a notice when it's text
`, Notices{License: "Linux-man-pages-copyleft", Copyright: []string{"Copyright (C) 1992 Drew Eckhardt", "Copyright (C) 1993 Michael Haardt, Ian Jackson"}}},
		{`.\"	$OpenBSD: nc.1,v 1.98 2024/04/01 12:40:18 deraadt Exp $
.\"
.\" Copyright (c) 1990, 1993
.\"	The Regents of the University of California.  All rights reserved.
.\" Copyright (c) 1996 David Sacerdote
.\" All rights reserved.
.\"
.\" 1. Redistributions of source code must retain the above copyright
.\"    notice, this list of conditions and the following disclaimer.
.Dd April 1, 2024
`, Notices{Copyright: []string{"Copyright (c) 1990, 1993 The Regents of the University of California. All rights reserved", "Copyright (c) 1996 David Sacerdote"}}},
		{"'\\\" t\n.\\\" © 2020 Someone\n.TH X 1\n", Notices{Copyright: []string{"© 2020 Someone"}}},
		{".TH NOTHING 1\n.SH NAME\nnothing\n", Notices{}},
	} {
		if got := FindNotices([]byte(c.src)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("FindNotices:\n got %#v\nwant %#v", got, c.want)
		}
	}
}
