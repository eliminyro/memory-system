package service

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

// FuzzParseMarkdown drives the markdown splitter with arbitrary content,
// asserting it never panics and never returns a section whose heading/body
// pairing is internally inconsistent in a way that would crash downstream.
// The property that matters is "no panic on any input" (malformed/adversarial
// markdown from store_memory or a bulk import). Run: go test -fuzz=FuzzParseMarkdown.
func FuzzParseMarkdown(f *testing.F) {
	for _, s := range []string{
		"", "# Title\n\n## H\nbody", "no headings, just prose",
		"### \n#### \n##### ", "#", strings.Repeat("#", 4000),
		"# a\n# b\n# c", "```\nunclosed fence\n", "\x00\x00\x00",
		"#\tTitle\r\n\r\nbody", strings.Repeat("# h\nx\n", 3000),
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, content string) {
		title, sections := parseMarkdown(content)
		_ = title
		// No panic is the property. Light sanity: parsed sections must be a
		// well-formed slice (len non-negative is trivially true; the real guard
		// is the absence of a panic/OOM/hang on adversarial input).
		_ = len(sections)
	})
}

// FuzzZipDocSource drives the in-memory zip importer with arbitrary bytes and
// then DRAINS the returned source, exercising both the header parse and the
// per-entry read loop (entry-count cap, decompressed-byte guard, empty-skip).
// The property is "no panic": a malformed/hostile archive must surface as an
// error, never crash the import worker. Run: go test -fuzz=FuzzZipDocSource.
func FuzzZipDocSource(f *testing.F) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if w, err := zw.Create("a.md"); err == nil {
		_, _ = w.Write([]byte("# T\n\nbody"))
	}
	if w, err := zw.Create("dir/b.md"); err == nil {
		_, _ = w.Write([]byte(""))
	}
	_ = zw.Close()
	f.Add(buf.Bytes())
	f.Add([]byte("not a zip at all"))
	f.Add([]byte{})
	f.Add([]byte("PK\x03\x04"))

	f.Fuzz(func(t *testing.T, archive []byte) {
		// Small decompressed ceiling so the byte-guard path is reachable on fuzz
		// input without buffering large data. The property is "no panic"; draining
		// exercises the header parse AND the per-entry read loop.
		src, _, err := zipDocSourceLimited(archive, 1<<16)
		if err != nil || src == nil {
			return
		}
		_ = src(func(path string, content []byte) error { return nil })
	})
}
