package bundle

import (
	"archive/tar"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/ir"
)

func TestCanonicalize(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`{"b":1,"a":[true,null,"x"]}`, "{\n  \"a\": [\n    true,\n    null,\n    \"x\"\n  ],\n  \"b\": 1\n}\n"},
		// Numbers as written; escapes only where needed, U+2028 and U+2029
		// escaped.
		{`[1.0,1e3,-0,12345678901234567890123]`, "[\n  1.0,\n  1e3,\n  -0,\n  12345678901234567890123\n]\n"},
		{`"A<>& \né"`, "\"A<>&\\u2028\\né\"\n"},
		// Keys sort by their UTF-8 bytes.
		{`{"é":1,"z":2,"Z":3}`, "{\n  \"Z\": 3,\n  \"z\": 2,\n  \"é\": 1\n}\n"},
		{`{}`, "{}\n"},
	} {
		got, err := Canonicalize([]byte(c.in))
		if err != nil || string(got) != c.want {
			t.Errorf("Canonicalize(%s) = %q, %v; want %q", c.in, got, err, c.want)
			continue
		}
		again, err := Canonicalize(got)
		if err != nil || !bytes.Equal(again, got) {
			t.Errorf("Canonicalize is not idempotent on %s: %q", c.in, again)
		}
	}
	for _, bad := range []string{``, `{`, `{} {}`, `[1,]`} {
		if _, err := Canonicalize([]byte(bad)); err == nil {
			t.Errorf("Canonicalize(%q) accepted", bad)
		}
	}
	// A repeated key keeps its last value, so such a text is never
	// canonical.
	if got, _ := Canonicalize([]byte(`{"a":1,"a":2}`)); string(got) != "{\n  \"a\": 2\n}\n" {
		t.Errorf("repeated key: %q", got)
	}
}

func TestManifestDeviations(t *testing.T) {
	if got, want := manifestDeviations([]string{"D12", "D5"}, engine.ModuleStateWarm),
		[]string{"D1", "D2", "D3", "D5", "D8", "D9", "D10", "D11", "D12", "D14", "D16", "D17"}; !slices.Equal(got, want) {
		t.Errorf("warm: %q, want %q", got, want)
	}
	if got, want := manifestDeviations([]string{"D7"}, engine.ModuleStateFresh),
		[]string{"D3", "D7", "D8", "D14", "D16", "D17"}; !slices.Equal(got, want) {
		t.Errorf("fresh: %q, want %q", got, want)
	}
}

// writeBundle writes a bundle directory by hand: its files and a manifest
// that lists them.
func writeBundle(t *testing.T, files map[string]string) (string, []byte) {
	t.Helper()
	dir := t.TempDir()
	m := testManifest()
	for p, text := range files {
		if err := writeFile(filepath.Join(dir, filepath.FromSlash(p)), []byte(text)); err != nil {
			t.Fatal(err)
		}
		m.Files[p] = Digest([]byte(text))
	}
	data, err := m.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Join(dir, ManifestFile), data); err != nil {
		t.Fatal(err)
	}
	return dir, data
}

func testManifest() *Manifest {
	return &Manifest{
		Format:          FormatVersion,
		Engine:          Engine{Version: "v0.0.0-test", ExpressionStackBytes: 1 << 20},
		Bundlers:        Bundlers{Bun: "1.3.10"},
		ModuleState:     engine.ModuleStateWarm,
		Deviations:      []string{"D3"},
		Contract:        ir.Contract{Roots: []ir.Root{{Flow: "f/a/flow"}}},
		HostModules:     []HostModuleEntry{},
		HarnessRuntimes: map[string]string{"bun": Digest([]byte("runtime"))},
		Sources:         map[string]string{"wmill.yaml": Digest([]byte("defaultTs: bun\n"))},
		Files:           map[string]string{},
	}
}

var testFiles = map[string]string{
	"ir.json":                     "{}\n",
	"scripts/bun/f/a/x.mjs":       "export const main = () => 1;\n",
	"scripts/bun/f/a/x.meta.json": "{}\n",
}

func archiveOf(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "b"+ArchiveExt)
	if err := WriteArchiveFile(dir, p); err != nil {
		t.Fatal(err)
	}
	return p
}

func mismatch(t *testing.T, err error, want ...string) {
	t.Helper()
	var me *MismatchError
	if !errors.As(err, &me) {
		t.Fatalf("want a mismatch, got %v", err)
	}
	for _, w := range want {
		if !strings.Contains(me.Error(), w) {
			t.Errorf("mismatch %q lacks %q", me.Error(), w)
		}
	}
}

func TestVerify(t *testing.T) {
	dir, manifest := writeBundle(t, testFiles)
	digest := Digest(manifest)
	wlb := archiveOf(t, dir)
	for _, a := range []string{dir, wlb} {
		v, err := Verify(a, VerifyOptions{Release: manifest, Digest: digest})
		if err != nil || v.Digest != digest || v.Manifest.Engine.Version != "v0.0.0-test" {
			t.Errorf("Verify(%s) = %+v, %v", a, v, err)
		}
	}
	if _, err := Verify(dir, VerifyOptions{Digest: "sha256:00"}); err == nil {
		t.Error("a wrong digest passed")
	} else {
		mismatch(t, err, "bundle digest "+digest+", want sha256:00")
	}

	t.Run("changed file", func(t *testing.T) {
		dir, _ := writeBundle(t, testFiles)
		os.WriteFile(filepath.Join(dir, "ir.json"), []byte("{\"x\":1}\n"), 0o644)
		_, err := Verify(dir, VerifyOptions{})
		mismatch(t, err, "ir.json: digest")
	})
	t.Run("extra and missing files", func(t *testing.T) {
		dir, _ := writeBundle(t, testFiles)
		os.WriteFile(filepath.Join(dir, "scripts", "extra.mjs"), nil, 0o644)
		os.Remove(filepath.Join(dir, "scripts", "bun", "f", "a", "x.mjs"))
		_, err := Verify(dir, VerifyOptions{})
		mismatch(t, err, "scripts/extra.mjs: not in the manifest", "scripts/bun/f/a/x.mjs: missing")
	})
	t.Run("a consistent forgery fails against the release", func(t *testing.T) {
		// The artifact's own manifest is rewritten to match a changed
		// file: it verifies by itself, but not against the release.
		files := map[string]string{}
		for k, v := range testFiles {
			files[k] = v
		}
		files["scripts/bun/f/a/x.mjs"] = "export const main = () => 2;\n"
		forged, _ := writeBundle(t, files)
		if _, err := Verify(forged, VerifyOptions{}); err != nil {
			t.Fatalf("the forgery does not verify by itself: %v", err)
		}
		_, err := Verify(forged, VerifyOptions{Release: manifest})
		mismatch(t, err, "manifest.json: digest", "scripts/bun/f/a/x.mjs: digest")
		_, err = Verify(archiveOf(t, forged), VerifyOptions{Digest: digest})
		mismatch(t, err, "bundle digest")
	})
	t.Run("symbolic link", func(t *testing.T) {
		dir, _ := writeBundle(t, testFiles)
		if err := os.Symlink("/etc/hosts", filepath.Join(dir, "link")); err != nil {
			t.Skip(err)
		}
		if _, err := Verify(dir, VerifyOptions{}); err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("a symbolic link: %v", err)
		}
	})
	t.Run("a link to the bundle", func(t *testing.T) {
		dir, manifest := writeBundle(t, testFiles)
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(dir, link); err != nil {
			t.Skip(err)
		}
		if _, err := Verify(link, VerifyOptions{Release: manifest}); err != nil {
			t.Errorf("a link to a bundle directory: %v", err)
		}
	})
	t.Run("manifest not canonical", func(t *testing.T) {
		dir, manifest := writeBundle(t, testFiles)
		os.WriteFile(filepath.Join(dir, ManifestFile), bytes.TrimSuffix(manifest, []byte("\n")), 0o644)
		if _, err := Verify(dir, VerifyOptions{}); err == nil || !strings.Contains(err.Error(), "canonical") {
			t.Errorf("a manifest without its final newline: %v", err)
		}
	})
}

func TestParseManifest(t *testing.T) {
	good, err := testManifest().Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, d, err := ParseManifest(good); err != nil || d != Digest(good) {
		t.Fatalf("ParseManifest = %s, %v", d, err)
	}
	edit := func(f func(*Manifest)) []byte {
		m := testManifest()
		f(m)
		b, err := m.Canonical()
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	for name, data := range map[string][]byte{
		"another format":      edit(func(m *Manifest) { m.Format = 2 }),
		"a path that escapes": edit(func(m *Manifest) { m.Files["../x"] = Digest(nil) }),
		"the manifest itself": edit(func(m *Manifest) { m.Files[ManifestFile] = Digest(nil) }),
		"two paths that differ only in case": edit(func(m *Manifest) {
			m.Files["scripts/bun/f/a.mjs"] = Digest(nil)
			m.Files["scripts/bun/f/A.mjs"] = Digest(nil)
		}),
		"a file that is a directory of another": edit(func(m *Manifest) {
			m.Files["ir.json"] = Digest(nil)
			m.Files["ir.json/x"] = Digest(nil)
		}),
		"a file that is a directory of another in another case": edit(func(m *Manifest) {
			m.Files["data/x.json"] = Digest(nil)
			m.Files["DATA"] = Digest(nil)
		}),
		"a file named like the manifest in another case": edit(func(m *Manifest) { m.Files["Manifest.json"] = Digest(nil) }),
		"a host-provided module without its copy": edit(func(m *Manifest) {
			m.HostModules = []HostModuleEntry{{Key: "f/a/ctx.ts", ID: "f/a/ctx"}}
		}),
		"a host-provided module id that escapes": edit(func(m *Manifest) {
			m.HostModules = []HostModuleEntry{{Key: "x", ID: "../x"}}
			m.Files["scripts/bun/provided/../x.mjs"] = Digest(nil)
		}),
		"an unknown field":   bytes.Replace(good, []byte(`"format": 1,`), []byte(`"extra": 1,`+"\n  "+`"format": 1,`), 1),
		"indented otherwise": bytes.ReplaceAll(good, []byte("  "), []byte("\t")),
		"not JSON":           []byte("format: 1\n"),
	} {
		if _, _, err := ParseManifest(data); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestTreeDigest(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, text string) {
		if err := writeFile(filepath.Join(dir, filepath.FromSlash(rel)), []byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	write("main.ts", "export {};\n")
	write("lib/a.ts", "export const a = 1;\n")
	want, err := TreeDigest(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Dot-files, dot-directories and node_modules are not the runtime.
	write(".DS_Store", "x")
	write("lib/.a.ts.swp", "x")
	write(".cache/x", "x")
	write("node_modules/p/index.js", "x")
	if got, _ := TreeDigest(dir); got != want {
		t.Errorf("ignored files changed the digest: %s, want %s", got, want)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err == nil {
		if got, err := TreeDigest(link); err != nil || got != want {
			t.Errorf("through a link: %s, %v", got, err)
		}
	}
	// A link below the root, to a file or to a directory, is refused: Bun
	// loads through it, and a digest that skipped it would stay the same
	// while what it points to changes.
	outside := t.TempDir()
	if err := writeFile(filepath.Join(outside, "frame.ts"), []byte("export {};\n")); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"frame.ts": filepath.Join(outside, "frame.ts"), "linked": outside} {
		tree := t.TempDir()
		copyTree(t, dir, tree)
		if err := os.Symlink(target, filepath.Join(tree, name)); err != nil {
			t.Skip(err)
		}
		if _, err := TreeDigest(tree); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Errorf("a link %s below the root: %v", name, err)
		}
	}
	write("lib/b.ts", "export const b = 2;\n")
	if got, _ := TreeDigest(dir); got == want {
		t.Error("a new file did not change the digest")
	}
	if _, err := TreeDigest(t.TempDir()); err == nil {
		t.Error("an empty directory has a digest")
	}
}

func TestWriteArchive(t *testing.T) {
	dir, manifest := writeBundle(t, testFiles)
	var a, b bytes.Buffer
	if err := WriteArchive(dir, &a); err != nil {
		t.Fatal(err)
	}
	if err := WriteArchive(dir, &b); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Error("one bundle gave two archives")
	}
	tr := tar.NewReader(bytes.NewReader(a.Bytes()))
	var names []string
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		names = append(names, hdr.Name)
		if hdr.Mode != 0o644 || !hdr.ModTime.Equal(time.Unix(0, 0)) || hdr.Uid != 0 || hdr.Gid != 0 || hdr.Uname != "" {
			t.Errorf("%s: header %+v", hdr.Name, hdr)
		}
	}
	if want := []string{ManifestFile, "ir.json", "scripts/bun/f/a/x.meta.json", "scripts/bun/f/a/x.mjs"}; !slices.Equal(names, want) {
		t.Errorf("entries %q, want %q", names, want)
	}

	// A file that no longer matches the manifest is not archived.
	os.WriteFile(filepath.Join(dir, "ir.json"), []byte("[]\n"), 0o644)
	if err := WriteArchive(dir, &bytes.Buffer{}); err == nil {
		t.Error("archived a changed file")
	}
	_ = manifest
}

// Extract writes a verified archive's bundle into a new directory, which
// then verifies with the archive's digest; it refuses a directory that is
// not empty and an archive that does not verify.
func TestExtract(t *testing.T) {
	dir, _ := writeBundle(t, testFiles)
	archive := filepath.Join(t.TempDir(), "b"+ArchiveExt)
	if err := WriteArchiveFile(dir, archive); err != nil {
		t.Fatal(err)
	}
	want, err := Verify(archive, VerifyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "x")
	v, err := Extract(archive, out, VerifyOptions{Digest: want.Digest})
	if err != nil {
		t.Fatal(err)
	}
	if v.Digest != want.Digest {
		t.Fatalf("digest %s, want %s", v.Digest, want.Digest)
	}
	for p, content := range testFiles {
		if got, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(p))); err != nil || string(got) != content {
			t.Errorf("%s: %q, %v", p, got, err)
		}
	}
	if _, err := Extract(archive, out, VerifyOptions{}); err == nil {
		t.Error("extracted into a directory that is not empty")
	}
	if _, err := Extract(archive, filepath.Join(t.TempDir(), "y"), VerifyOptions{Digest: "sha256:" + strings.Repeat("0", 64)}); err == nil {
		t.Error("extracted an archive whose digest is not the pinned one")
	}
}

// TestVerifyArchiveEntries checks the archive reader's refusals and that
// the order of entries does not matter.
func TestVerifyArchiveEntries(t *testing.T) {
	_, manifest := writeBundle(t, testFiles)
	type entry struct {
		name, body string
		typ        byte
	}
	write := func(entries []entry) string {
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		for _, e := range entries {
			hdr := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: e.typ}
			if e.typ == tar.TypeSymlink {
				hdr.Size, hdr.Linkname = 0, "/etc/hosts"
			}
			if e.typ == tar.TypeDir {
				hdr.Size = 0
			}
			if err := tw.WriteHeader(hdr); err != nil {
				t.Fatal(err)
			}
			if hdr.Size > 0 {
				tw.Write([]byte(e.body))
			}
		}
		tw.Close()
		p := filepath.Join(t.TempDir(), "b.wlb")
		os.WriteFile(p, buf.Bytes(), 0o644)
		return p
	}
	files := []entry{{"./scripts/", "", tar.TypeDir}}
	for p, text := range testFiles {
		files = append(files, entry{p, text, tar.TypeReg})
	}
	// The manifest last, entries in map order, a directory entry and a
	// leading "./": still the same bundle.
	if _, err := Verify(write(append(files, entry{ManifestFile, string(manifest), tar.TypeReg})), VerifyOptions{Release: manifest}); err != nil {
		t.Errorf("reordered archive: %v", err)
	}
	// Ordinary directory headers, the root's included, are fine.
	withDirs := append([]entry{{"./", "", tar.TypeDir}, {"scripts", "", tar.TypeDir}, {"./scripts/bun/", "", tar.TypeDir}}, files...)
	if _, err := Verify(write(append(withDirs, entry{ManifestFile, string(manifest), tar.TypeReg})), VerifyOptions{Digest: Digest(manifest)}); err != nil {
		t.Errorf("directory headers: %v", err)
	}
	// Directory entries are checked as files are, and the entries must form
	// one tree: extracting a directory named ir.json would replace the
	// file the manifest authenticates. Each holds against a release
	// manifest and an expected digest, the directory before or after the
	// file.
	irDir := entry{"ir.json/", "", tar.TypeDir}
	for name, entries := range map[string][]entry{
		"a directory over a file, after it":   append(slices.Clone(files), irDir),
		"a directory over a file, before it":  append([]entry{irDir}, files...),
		"a directory named like the manifest": append(slices.Clone(files), entry{ManifestFile, "", tar.TypeDir}),
		"a directory that escapes":            append(slices.Clone(files), entry{"../outside/", "", tar.TypeDir}),
		"an absolute directory":               append(slices.Clone(files), entry{"/absolute-dir/", "", tar.TypeDir}),
		"a directory that is not clean":       append(slices.Clone(files), entry{"scripts//bun/", "", tar.TypeDir}),
		"a file below a file":                 append(slices.Clone(files), entry{"ir.json/x", "{}\n", tar.TypeReg}),
		"a directory below a file":            append(slices.Clone(files), entry{"ir.json/sub/", "", tar.TypeDir}),
		// Where names compare without case, as on macOS, these alias
		// ir.json or scripts/ when extracted.
		"a directory over a file in another case, after it":  append(slices.Clone(files), entry{"IR.JSON/", "", tar.TypeDir}),
		"a directory over a file in another case, before it": append([]entry{{"IR.JSON/", "", tar.TypeDir}}, files...),
		"a directory below a file in another case":           append(slices.Clone(files), entry{"IR.JSON/sub/", "", tar.TypeDir}),
		"a file in another case":                             append(slices.Clone(files), entry{"IR.JSON", "{}\n", tar.TypeReg}),
		"a directory in another case":                        append(slices.Clone(files), entry{"Scripts/", "", tar.TypeDir}),
		"a directory the bundle does not have":               append(slices.Clone(files), entry{"extra/", "", tar.TypeDir}),
	} {
		p := write(append(entries, entry{ManifestFile, string(manifest), tar.TypeReg}))
		for _, opts := range []VerifyOptions{{Release: manifest}, {Digest: Digest(manifest)}} {
			if _, err := Verify(p, opts); err == nil {
				t.Errorf("%s: accepted", name)
			}
		}
	}
	for name, extra := range map[string]entry{
		"a repeated entry":    {"ir.json", "{}\n", tar.TypeReg},
		"a path that escapes": {"../ir.json", "{}\n", tar.TypeReg},
		"an absolute path":    {"/ir.json", "{}\n", tar.TypeReg},
		"a symbolic link":     {"link", "", tar.TypeSymlink},
	} {
		p := write(append(append([]entry{{ManifestFile, string(manifest), tar.TypeReg}}, files...), extra))
		if _, err := Verify(p, VerifyOptions{}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Verify(write(files), VerifyOptions{}); err == nil || !strings.Contains(err.Error(), "no manifest.json") {
		t.Errorf("no manifest: %v", err)
	}
}

// TestFoldKey checks that two names share a fold key exactly when they are
// equal under Unicode case folding.
func TestFoldKey(t *testing.T) {
	names := []string{"ir.json", "IR.JSON", "Ir.Json", "ir.jsn", "k", "K", "\u212a", "ß", "\u1e9e", "ss", "σ", "Σ", "ς", "é", "É", "e\u0301"}
	for _, a := range names {
		for _, b := range names {
			if (foldKey(a) == foldKey(b)) != strings.EqualFold(a, b) {
				t.Errorf("foldKey(%q) and foldKey(%q): equal %v, EqualFold %v", a, b, foldKey(a) == foldKey(b), strings.EqualFold(a, b))
			}
		}
	}
}
