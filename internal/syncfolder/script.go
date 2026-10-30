package syncfolder

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/invakid404/windmill-lite/internal/openflow"
	"github.com/invakid404/windmill-lite/internal/yamlx"
)

// Script is a path script as the CLI would push it (SY-2 to SY-5, SY-8).
type Script struct {
	Path string `json:"path"`
	// File is the source file, root-relative.
	File string `json:"file"`
	// Language is the language a deploy records: the one the file name
	// gives (SY-3), with a Bun script that carries the `native` annotation
	// switched to bunnative (SY-V3).
	Language openflow.Language `json:"language"`
	// Native reports the `native` annotation of a script whose file name
	// gives bun. It is false for every other language.
	Native  bool   `json:"native"`
	Content string `json:"content"`
	// MetadataFile is the `.script.yaml` file, "" when the script has none
	// and Metadata holds the CLI's defaults.
	MetadataFile string         `json:"metadata_file,omitempty"`
	Metadata     ScriptMetadata `json:"metadata"`
	// Lock is the lock text, nil when the metadata has no lock (SY-5).
	Lock *string `json:"lock"`
	// LockFile is the file the lock was read from, "" when it was written
	// in the metadata or the referenced file is missing or unreadable.
	LockFile string `json:"lock_file,omitempty"`
	// Codebase is the codebases entry that selects the script, nil when
	// none does (SY-8).
	Codebase *Codebase `json:"codebase"`
	// Warnings are what the CLI warns about while it pushes the script: a
	// lock reference whose file is missing or unreadable (SY-5, SY-19).
	Warnings []string `json:"warnings,omitempty"`
}

// ScriptMetadata holds the `.script.yaml` keys windmill-lite uses (SY-4).
// The others are kept in Raw; keys that are out of scope (tag, priority,
// concurrency, debouncing, permissions) have no effect.
type ScriptMetadata struct {
	Summary     string `json:"summary"`
	Description string `json:"description"`
	// Schema is the JSON Schema of main's arguments, verbatim; nil when a
	// metadata file has none. It is nil too without a metadata file: the
	// push then infers the schema from main's parameters (SY-4), which the
	// loader does not do.
	Schema                 json.RawMessage `json:"schema"`
	Kind                   ScriptKind      `json:"kind"`
	Envs                   []string        `json:"envs"`
	Timeout                *int32          `json:"timeout"`
	CacheTTL               *int32          `json:"cache_ttl"`
	DeleteAfterSecs        *int32          `json:"delete_after_secs"`
	RestartUnlessCancelled *bool           `json:"restart_unless_cancelled"`
	HasPreprocessor        *bool           `json:"has_preprocessor"`
	// Raw is the whole metadata as the JSON the CLI sends; nil without a
	// metadata file.
	Raw json.RawMessage `json:"raw"`
}

// ScriptKind is a script's `kind` (SY-4).
type ScriptKind string

const (
	ScriptKindScript       ScriptKind = "script"
	ScriptKindTrigger      ScriptKind = "trigger"
	ScriptKindFailure      ScriptKind = "failure"
	ScriptKindApproval     ScriptKind = "approval"
	ScriptKindPreprocessor ScriptKind = "preprocessor"
)

// languageSuffixes are SY-3's file-name tests, in order; the first match
// wins. `fetch.ts`, `bun.ts` and `deno.ts` have no leading dot, so
// `mybun.ts` is bun. `.ts` gives defaultTs, marked by the empty language.
var languageSuffixes = []struct {
	suffix string
	lang   openflow.Language
}{
	{".py", openflow.LangPython3},
	{"fetch.ts", openflow.LangNativeTS},
	{"bun.ts", openflow.LangBun},
	{"deno.ts", openflow.LangDeno},
	{".ts", ""},
	{".go", openflow.LangGo},
	{".my.sql", openflow.LangMySQL},
	{".bq.sql", openflow.LangBigQuery},
	{".odb.sql", openflow.LangOracleDB},
	{".duckdb.sql", openflow.LangDuckDB},
	{".sf.sql", openflow.LangSnowflake},
	{".ms.sql", openflow.LangMSSQL},
	{".pg.sql", openflow.LangPostgreSQL},
	{".gql", openflow.LangGraphQL},
	{".sh", openflow.LangBash},
	{".ps1", openflow.LangPowerShell},
	{".php", openflow.LangPHP},
	{".rs", openflow.LangRust},
	{".cs", openflow.LangCSharp},
	{".playbook.yml", openflow.LangAnsible},
	{".nu", openflow.LangNu},
	{".java", openflow.LangJava},
	{".rb", openflow.LangRuby},
	{".r", openflow.LangRLang},
}

// scriptExtensions are the extensions of script source files (SY-18): the
// last extensions `.ts`, `.py`, `.go`, `.sh`, `.sql`, `.gql`, `.ps1`,
// `.js`, `.php`, `.rs`, `.cs`, `.nu`, `.java`, `.rb` and `.r`, and the name
// ending `.playbook.yml`, with SY-3's multi-part ones so that the logical
// path drops them whole. A bare `.sql` fails with SY-3's dialect hint and a
// `.js` file with the CLI's type error, rather than being ignored.
var scriptExtensions = []string{
	".py", ".fetch.ts", ".bun.ts", ".deno.ts", ".ts", ".go", ".my.sql", ".bq.sql", ".odb.sql",
	".duckdb.sql", ".sf.sql", ".ms.sql", ".pg.sql", ".sql", ".gql", ".sh", ".ps1", ".js", ".php", ".rs",
	".cs", ".playbook.yml", ".nu", ".java", ".rb", ".r",
}

// scriptExtension returns the longest script extension of a file name, or
// "" when the file is not a script source.
func scriptExtension(name string) string {
	best := ""
	for _, ext := range scriptExtensions {
		if strings.HasSuffix(name, ext) && len(ext) > len(best) {
			best = ext
		}
	}
	return best
}

// cliScriptPath is the Windmill path the CLI gives a script source file:
// everything before the first dot of its root-relative path (SY-1).
func cliScriptPath(file string) string {
	if i := strings.IndexByte(file, '.'); i >= 0 {
		return file[:i]
	}
	return file
}

// logicalScriptPath is the file's path without its script extension. It
// differs from cliScriptPath when a folder or file name has a dot.
func logicalScriptPath(file string) string {
	return strings.TrimSuffix(file, scriptExtension(path.Base(file)))
}

// inferLanguage applies SY-3's tests to a source file name.
func inferLanguage(name string, defaultTs openflow.Language) (openflow.Language, error) {
	for _, s := range languageSuffixes {
		if strings.HasSuffix(name, s.suffix) {
			if s.lang == "" {
				return defaultTs, nil
			}
			return s.lang, nil
		}
	}
	msg := fmt.Sprintf("cannot infer script language from extension of %s", name)
	if strings.HasSuffix(name, ".sql") {
		msg += "; a SQL script needs a dialect suffix: .pg.sql, .my.sql, .bq.sql, .odb.sql, .duckdb.sql, .sf.sql or .ms.sql"
	}
	return "", &Error{Kind: ErrLanguage, Msg: msg}
}

// Script reads the script at a Windmill path.
func (f *Folder) Script(p string) (*Script, error) {
	e, err := f.lookup(KindScript, p)
	if err != nil {
		return nil, err
	}
	item := "script " + p
	s := &Script{Path: p, File: e.file}
	if strings.HasSuffix(e.file, ".js") {
		// A source by its extension (SY-18), but no push type matches it.
		return nil, &Error{Kind: ErrLanguage, Item: item, File: e.file,
			Msg: fmt.Sprintf("The item %s has an unrecognized type ending script", e.file)}
	}
	lang, err := inferLanguage(path.Base(e.file), f.Config.DefaultTs)
	if err != nil {
		le := err.(*Error)
		le.File, le.Item = e.file, item
		return nil, le
	}
	if s.Content, err = f.readText(e.file, item); err != nil {
		return nil, err
	}
	s.Language = lang
	if lang == openflow.LangBun {
		if s.Native = HasNativeAnnotation(s.Content); s.Native {
			s.Language = openflow.LangBunNative
		}
		s.Codebase = f.Config.codebase(e.file)
	}
	if err := f.readMetadata(s, e.meta); err != nil {
		return nil, err.withItem(item)
	}
	return s, nil
}

// readText reads a source file through the decoder (SY-19); a refused
// file fails the push. It must be valid UTF-8.
func (f *Folder) readText(file, item string) (string, error) {
	data, found, refused, err := f.readDecoded(file)
	switch {
	case err != nil:
		return "", err.withItem(item)
	case !found:
		return "", &Error{Kind: ErrIO, Item: item, File: file, Msg: "the file is missing"}
	case refused != nil:
		return "", refused.withItem(item)
	}
	return checkText(data, file, item)
}

func (f *Folder) readMetadata(s *Script, file string) *Error {
	md := &s.Metadata
	md.Kind = ScriptKindScript
	if file == "" {
		// A script without metadata (SY-4). The push gives it a default
		// file (empty summary and description, kind script, and the lock
		// `!inline <p>.script.lock`), then regenerates the schema from
		// main's parameters and the lock, which windmill-lite does not do:
		// the schema stays nil, and the lock is <p>.script.lock as the
		// default file names it. The CLI writes that file empty when it is
		// missing, so a missing one is not warned about.
		name := s.Path + ".script.lock"
		lock, lockFile, unreadable, err := f.readLockFile(name)
		if err != nil {
			return err
		}
		s.Lock, s.LockFile = &lock, lockFile
		if unreadable != nil {
			s.Warnings = append(s.Warnings, inlinePathWarning(name, unreadable))
		}
		return nil
	}
	s.MetadataFile = file
	data, found, refused, rerr := f.readDecoded(file)
	switch {
	case rerr != nil:
		return rerr
	case !found:
		return &Error{Kind: ErrIO, File: file, Msg: "the file is missing"}
	case refused != nil:
		// The CLI then tries the __mod metadata files, which a flat
		// script does not have here (SY-2), and fails.
		return refused
	}
	m, err := parseMapping(file, ErrMetadata, data)
	if err != nil {
		return err.(*Error)
	}
	md.Raw = m.m.AppendJSON(nil)
	for _, sf := range []struct {
		key string
		dst *string
	}{{"summary", &md.Summary}, {"description", &md.Description}} {
		v, err := m.str(sf.key)
		if err != nil {
			return err.(*Error)
		}
		if v != nil {
			*sf.dst = *v
		}
	}
	md.Schema = m.raw("schema")
	if k, err := m.str("kind"); err != nil {
		return err.(*Error)
	} else if k != nil {
		switch kind := ScriptKind(*k); kind {
		case ScriptKindScript, ScriptKindTrigger, ScriptKindFailure, ScriptKindApproval, ScriptKindPreprocessor:
			md.Kind = kind
		default:
			return m.wrong(m.get("kind"), "kind", "one of script, trigger, failure, approval and preprocessor")
		}
	}
	if md.Envs, err = m.strings("envs"); err != nil {
		return err.(*Error)
	}
	for _, i := range []struct {
		key string
		dst **int32
	}{{"timeout", &md.Timeout}, {"cache_ttl", &md.CacheTTL}, {"delete_after_secs", &md.DeleteAfterSecs}} {
		if *i.dst, err = m.int32(i.key); err != nil {
			return err.(*Error)
		}
	}
	for _, b := range []struct {
		key string
		dst **bool
	}{{"restart_unless_cancelled", &md.RestartUnlessCancelled}, {"has_preprocessor", &md.HasPreprocessor}} {
		if *b.dst, err = m.optBool(b.key); err != nil {
			return err.(*Error)
		}
	}
	return f.readLock(s, m)
}

// readLock reads `lock` (SY-5): a list of strings is joined with `\n`
// first; then a string that starts with exactly `!inline ` is a reference
// to the file named by its second space-separated token, relative to the
// sync root, and any other string is the lock as written. A missing or
// unreadable file is the empty lock, with the CLI's warning
// ([inlinePathWarning]).
func (f *Folder) readLock(s *Script, m fields) *Error {
	n := m.get("lock")
	if n == nil {
		return nil
	}
	var lock string
	switch n.Kind {
	case yamlx.String:
		lock = n.Text
	case yamlx.Sequence:
		lines := make([]string, 0, len(n.Items))
		for _, it := range n.Items {
			if it.Kind != yamlx.String {
				return m.wrong(it, "lock entry", "a string")
			}
			lines = append(lines, it.Text)
		}
		lock = strings.Join(lines, "\n")
	default:
		return m.wrong(n, "lock", "a string or a list of strings")
	}
	name, ok := scriptLockFile(lock)
	if !ok {
		s.Lock = &lock
		return nil
	}
	p, err := rootPath(m.file, name)
	if err != nil {
		return err
	}
	lock, lockFile, unreadable, err := f.readLockFile(p)
	if err != nil {
		return err
	}
	s.Lock, s.LockFile = &lock, lockFile
	if lockFile == "" {
		s.Warnings = append(s.Warnings, inlinePathWarning(name, unreadable))
	}
	return nil
}

// inlinePathWarning is the CLI's warning for a script lock reference whose
// file it cannot read (SY-5): `Error reading inline path: <name>, <error>`,
// where name is the reference as written and error is the read error as
// the published CLI, on Node, prints it. A missing file (unreadable nil)
// gives `Error: ENOENT: no such file or directory, open '<name>'`, and a
// file the decoder refuses gives `Error: ` and the decoder's message
// (SY-19). The reference gives no text for any other failure, such as a
// file without read permission; windmill-lite then gives `Error: ` and the
// failure in Go's words. Both the decoder's message and Go's error name the
// file root-relative, which is name for any reference without `.`, `..`
// or empty segments.
func inlinePathWarning(name string, unreadable *Error) string {
	cause := fmt.Sprintf("ENOENT: no such file or directory, open '%s'", name)
	if unreadable != nil {
		cause = unreadable.Msg
	}
	return fmt.Sprintf("Error reading inline path: %s, Error: %s", name, cause)
}

// scriptLockFile recognises a `.script.yaml` lock reference (SY-5): a
// string that starts with exactly `!inline ` (with the space), without the
// leading white space a flow's inline lock may have (SY-V1). The file is
// its second space-separated token.
func scriptLockFile(s string) (string, bool) {
	if !strings.HasPrefix(s, "!inline ") {
		return "", false
	}
	return secondToken(s)
}

// readLockFile reads a lock file through the decoder. A file that is
// missing, or that cannot be read, the decoder's refusal included, is the
// empty lock, and lockFile is then "" (SY-5, SY-7, SY-19); unreadable is
// the failure, nil for a missing file, for the caller's warning. A file the
// walk reads fails the push instead ([Folder.readReference]).
func (f *Folder) readLockFile(p string) (lock, lockFile string, unreadable, err *Error) {
	data, found, unreadable, err := f.readReference(p)
	if err != nil || unreadable != nil || !found {
		return "", "", unreadable, err
	}
	text, terr := checkText(data, p, "")
	if terr != nil {
		return "", "", nil, terr.(*Error)
	}
	return text, p, nil, nil
}

// rootPath resolves a reference relative to the sync root. A reference
// that leaves the root, or an absolute one, is refused: the CLI would read
// it, windmill-lite does not (ADR 0002).
func rootPath(from, name string) (string, *Error) {
	p := path.Join(".", name)
	if p == ".." || strings.HasPrefix(p, "../") || strings.HasPrefix(name, "/") {
		return "", &Error{Kind: ErrPathEscape, File: from,
			Msg: fmt.Sprintf("the reference %q leaves the sync root, which windmill-lite refuses (ADR 0002)", name)}
	}
	return p, nil
}
