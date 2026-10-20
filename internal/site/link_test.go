package site

import (
	"strings"
	"testing"
)

func TestLinkCase(t *testing.T) {
	b := &builder{byName: map[string][]string{}, byLower: map[string][]string{}}
	for _, p := range [][2]string{{"pcre", "3"}, {"SSL_read", "3ssl"}, {"Xvfb", "1"}, {"XVFB", "1"}, {"ls", "1"}} {
		if _, ok := b.byName[p[0]]; !ok {
			low := strings.ToLower(p[0])
			b.byLower[low] = append(b.byLower[low], p[0])
		}
		b.byName[p[0]] = append(b.byName[p[0]], p[1])
	}
	for _, c := range []struct{ name, sec, want string }{
		{"pcre", "3", "pcre"},
		{"PCRE", "3", "pcre"},
		{"ssl_read", "3", "SSL_read"},
		{"XVFB", "1", "XVFB"},
		{"xvfb", "1", "Xvfb"},
		{"LS", "8", ""},
		{"nope", "1", ""},
	} {
		got := b.link(c.name, c.sec)
		if want := ""; c.want != "" {
			want = b.pageURL(c.want, b.byName[c.want][0])
			if got != want {
				t.Errorf("%s(%s): %q, want %q", c.name, c.sec, got, want)
			}
		} else if got != want {
			t.Errorf("%s(%s): %q, want none", c.name, c.sec, got)
		}
	}
}
