package ir

import (
	"github.com/invakid404/windmill-lite/internal/openflow"
	"github.com/invakid404/windmill-lite/internal/syncfolder"
)

// Class is a harness class: the harness that runs a leaf. Placement is
// decided per leaf by the pool key, of which the class is a part (0004).
type Class string

// The harness classes of 0002's table. Only Bun exists in the first
// release; the others are the later harnesses of milestone L1.
const (
	ClassBun     Class = "bun"
	ClassPython3 Class = "python3"
	ClassDeno    Class = "deno"
	ClassGo      Class = "go"
	ClassBash    Class = "bash"
)

// Boundary is how a harness runs a leaf: with its language's own rules, or
// in the Bun harness's native mode (deviation D5).
type Boundary string

const (
	// BoundaryBun follows Windmill's Bun wrapper (WR-13, WR-18).
	BoundaryBun Boundary = "bun"
	// BoundaryNative follows Windmill's native runtime (WR-40 to WR-46),
	// emulated in the Bun harness (D5).
	BoundaryNative Boundary = "native"
	// BoundaryOwn is the boundary of a later harness's own language
	// (WR-33 to WR-35).
	BoundaryOwn Boundary = "own"
)

// harnessOf maps a step's language to its harness class and boundary
// (0002, Language and harness class). ok is false for a language with no
// class.
func harnessOf(l openflow.Language) (c Class, b Boundary, ok bool) {
	switch l {
	case openflow.LangBun:
		return ClassBun, BoundaryBun, true
	case openflow.LangBunNative, openflow.LangNativeTS:
		return ClassBun, BoundaryNative, true
	case openflow.LangPython3:
		return ClassPython3, BoundaryOwn, true
	case openflow.LangDeno:
		return ClassDeno, BoundaryOwn, true
	case openflow.LangGo:
		return ClassGo, BoundaryOwn, true
	case openflow.LangBash:
		return ClassBash, BoundaryOwn, true
	}
	return "", "", false
}

// BoundaryOf returns the boundary a step of language l runs with, the
// language a deploy records (DeployedLanguage), and false for a language
// with no harness class (0002).
func BoundaryOf(l openflow.Language) (Boundary, bool) {
	_, b, ok := harnessOf(l)
	return b, ok
}

// DeployedLanguage is the language a deploy records for a rawscript of
// language l with source content: a Bun-family one follows its `native`
// header annotation, in both directions, whatever it said (OF-15, SY-V3,
// WR-40); any other keeps l. The sync-folder loader applies the same rule
// to path scripts (SY-3).
func DeployedLanguage(l openflow.Language, content string) openflow.Language {
	if l != openflow.LangBun && l != openflow.LangBunNative {
		return l
	}
	if syncfolder.HasNativeAnnotation(content) {
		return openflow.LangBunNative
	}
	return openflow.LangBun
}

// available reports whether this release has a harness for class c.
func available(c Class) bool { return c == ClassBun }

// bunFamily reports whether l is one of the TypeScript languages whose
// header annotations Windmill reads (WR-31).
func bunFamily(l openflow.Language) bool {
	switch l {
	case openflow.LangBun, openflow.LangBunNative, openflow.LangNativeTS:
		return true
	}
	return false
}
