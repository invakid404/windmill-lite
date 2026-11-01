package shapes

import (
	"encoding/base64"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
)

// Tier is one production size of a brief family: typical, p90 or worst.
type Tier struct {
	Name string
	Size int
}

// BriefFamily is one of the eight shape families of the workload brief
// (F1 to F8), at its three sizes. The shapes follow the brief's tables and
// its suggested synthetic shapes; the sizes are the middle of each range,
// or the suggested shape's size for the worst case.
type BriefFamily struct {
	Name  string
	Tiers []Tier
	Make  func(size int) []byte
}

const kib = 1 << 10

// Brief are the brief's families.
var Brief = []BriefFamily{
	{"F1", []Tier{{"typical", 96 * kib}, {"p90", 448 * kib}, {"worst", 2100 * kib}}, f1},
	{"F2", []Tier{{"typical", 8 * kib}, {"p90", 64 * kib}, {"worst", 2150 * kib}}, f2},
	{"F3", []Tier{{"typical", 8 * kib}, {"p90", 48 * kib}, {"worst", 256 * kib}}, f3},
	{"F4", []Tier{{"typical", 24 * kib}, {"p90", 96 * kib}, {"worst", 1024 * kib}}, f4},
	{"F5", []Tier{{"typical", 12 * kib}, {"p90", 48 * kib}, {"worst", 2048 * kib}}, f5},
	{"F6", []Tier{{"typical", 128 * kib}, {"p90", 1024 * kib}, {"worst", 6144 * kib}}, f6},
	{"F7", []Tier{{"typical", 32 * kib}, {"p90", 512 * kib}, {"worst", 4096 * kib}}, f7},
	{"F8", []Tier{{"typical", 1024 * kib}, {"p90", 8192 * kib}, {"worst", 30720 * kib}}, f8},
}

// BriefNamed is the brief family called name.
func BriefNamed(name string) (BriefFamily, bool) {
	for _, f := range Brief {
		if f.Name == name {
			return f, true
		}
	}
	return BriefFamily{}, false
}

// The texts below are JSON string contents as JSON.stringify writes them:
// quotes, backslashes and line breaks escaped, everything else raw UTF-8.

// markupChunk is HTML-like markup with attributes, entities and a little
// non-ASCII text, as a mail body or a scraped page holds it.
const markupChunk = `<div class=\"msg\" data-id=\"m1\"><p style=\"margin:0 0 8px 0\">Hello Ana,</p>\n` +
	`<p>Thanks for the update on the quarterly figures &mdash; the café meeting moved to Thursday. ` +
	`Please find the summary below and the “draft 3” attached.</p>\n<table border=\"0\" cellpadding=\"4\">` +
	`<tr><td>Region</td><td>Revenue</td><td>Δ</td></tr><tr><td>EU-1</td><td>1,204,000</td><td>+4.2%</td></tr></table>\n` +
	`<blockquote>On Tue, Bob wrote:<br>&gt; Could you resend the numbers for Zürich and São Paulo?</blockquote></div>\n`

// plainChunk is the plain-text form of markup, or text derived from it.
const plainChunk = `Hello Ana,\n\nThanks for the update on the quarterly figures — the café meeting moved to Thursday. ` +
	`Please find the summary below and the “draft 3” attached.\n\nRegion  Revenue  Δ\nEU-1  1,204,000  +4.2%\n\n` +
	`> On Tue, Bob wrote:\n> Could you resend the numbers for Zürich and São Paulo?\n\n`

// textOf is n bytes of JSON string content repeating chunk, cut at a space
// so that no escape or character is split, the rest filled with dots.
func textOf(chunk string, n int) string {
	if n <= 0 {
		return ""
	}
	s := strings.Repeat(chunk, n/len(chunk)+1)[:n]
	if i := strings.LastIndexByte(s, ' '); i > 0 {
		s = s[:i]
	}
	return s + strings.Repeat(".", n-len(s))
}

// jw writes JSON text; long strings are added as content already escaped.
type jw struct{ b []byte }

func (w *jw) raw(s string) *jw { w.b = append(w.b, s...); return w }
func (w *jw) str(s string) *jw {
	w.b = append(w.b, '"')
	w.b = append(w.b, s...)
	w.b = append(w.b, '"')
	return w
}
func (w *jw) key(k string) *jw { return w.str(k).raw(":") }
func (w *jw) int(n int) *jw    { w.b = strconv.AppendInt(w.b, int64(n), 10); return w }

// exact builds a value whose last long string has the content filler(n),
// n chosen so that the text is exactly size bytes: build(text) must make
// the whole text around it.
func exact(size int, chunk string, build func(text string) []byte) []byte {
	base := len(build(""))
	n := size - base
	if n < 0 {
		panic(fmt.Sprintf("bench: a brief shape of %d bytes does not fit in %d", base, size))
	}
	out := build(textOf(chunk, n))
	if len(out) != size {
		panic(fmt.Sprintf("bench: a brief shape came out at %d bytes, not %d", len(out), size))
	}
	return out
}

// headerMap is F1's map of keys, each an array of 1 to 5 strings of 50 to
// 500 bytes.
func headerMap(w *jw, keys int, r *rand.Rand) {
	w.raw("{")
	for i := range keys {
		if i > 0 {
			w.raw(",")
		}
		w.key("x-header-" + strconv.Itoa(i))
		w.raw("[")
		for j := range 1 + r.IntN(5) {
			if j > 0 {
				w.raw(",")
			}
			w.str(textOf("value; token=abc123; path=/a/b; domain=example.invalid ", 50+r.IntN(200)))
		}
		w.raw("]")
	}
	w.raw("}")
}

// smallRecords is F1's array of n records of 6 keys.
func smallRecords(w *jw, n int) {
	w.raw("[")
	for i := range n {
		if i > 0 {
			w.raw(",")
		}
		w.raw(`{"id":"att-`).int(i).raw(`","name":"report-`).int(i).raw(`.pdf","type":"application/pdf","size":`).int(10000 + i*977).raw(`,"inline":false,"cid":"part`).int(i).raw(`@example.invalid"}`)
	}
	w.raw("]")
}

// f1 is one wide object of 22 keys with a markup string and its plain-text
// form (about 0.27 of it), a map of header arrays and a small-record array.
// Above 1 MiB a third of the markup is an inline base64 image.
func f1(size int) []byte {
	r := rand.New(rand.NewPCG(11, 1))
	keys := 40
	if size < 256*kib {
		keys = 20
	}
	var hdr jw
	headerMap(&hdr, keys, r)
	var att jw
	smallRecords(&att, 8)
	fixed := 600 + len(hdr.b) + len(att.b)
	markupLen := (size - fixed) * 100 / 127
	img := ""
	if size > 1<<20 {
		raw := make([]byte, markupLen/3*3/4)
		for i := range raw {
			raw[i] = byte(r.Uint32())
		}
		img = `<img alt=\"chart\" src=\"data:image/png;base64,` + base64.StdEncoding.EncodeToString(raw) + `\">\n`
	}
	markup := img + textOf(markupChunk, markupLen-len(img))
	return exact(size, plainChunk, func(text string) []byte {
		w := &jw{}
		w.raw(`{"id":"msg-000123","thread_id":"thr-000045","subject":"Re: quarterly report — draft 3","from":"Ana Example <ana@example.invalid>",`)
		w.raw(`"to":["ops@example.invalid","finance@example.invalid"],"cc":[],"reply_to":"ana@example.invalid","date":"2026-10-30T09:15:00Z",`)
		w.raw(`"folder":"inbox","language":"en","status":"new","category":"finance","priority":2,"size_hint":123456,"has_attachments":true,`)
		w.raw(`"source":"mail","labels":["finance","q3"],"summary":"Quarterly figures and the café meeting",`)
		w.key("headers").raw(string(hdr.b)).raw(",")
		w.key("attachments").raw(string(att.b)).raw(",")
		w.key("markup").str(markup).raw(",")
		w.key("text").str(text)
		return w.raw("}").b
	})
}

// f2Record is one F2 record: 24 keys, depth 4, six small arrays of 1 to 3
// objects of 1 or 2 keys, two nested objects, 2 numbers, 2 booleans, a null
// and the text field.
func f2Record(w *jw, i int, text string) {
	w.raw(`{"id":"rec-`).int(i).raw(`","external_id":"ext-`).int(100000 + i).raw(`","type":"conversation","title":"Follow-up on order `).int(5000 + i).raw(`",`)
	w.raw(`"author":{"id":"u-`).int(i % 7).raw(`","name":"Agent `).int(i % 7).raw(`","role":"agent"},"channel":{"id":"c-1","name":"support","kind":"email","region":"eu-1"},`)
	w.raw(`"created_at":"2026-10-01T08:00:00Z","updated_at":"2026-10-02T09:30:00Z","score":0.82,"rank":`).int(i).raw(`,"is_read":true,"is_flagged":false,"parent_id":null,`)
	w.raw(`"tags":[{"name":"billing"},{"name":"priority","weight":2}],"participants":[{"id":"u-1"},{"id":"u-2","role":"cc"}],"links":[{"href":"https://example.invalid/t/`).int(i).raw(`"}],`)
	w.raw(`"labels":[{"k":"lang","v":"en"}],"mentions":[{"id":"u-3"},{"id":"u-4"},{"id":"u-5"}],"attachments_meta":[{"name":"a.pdf","size":"12 KB"}],`)
	w.raw(`"status":"open","locale":"en","summary":"Customer asks about the invoice for order `).int(5000 + i).raw(`","source":"mail",`)
	w.key("text").str(text).raw("}")
}

// f2 is an array of N wide records with one derived-text field each: N is
// about size / 21.5 KiB (1 for the typical size, 100 for the worst).
func f2(size int) []byte {
	n := max(1, (size+11*kib)/(21*kib+512))
	var probe jw
	f2Record(&probe, 0, "")
	per := (size-2)/n - len(probe.b) - 1
	return exact(size, plainChunk, func(last string) []byte {
		w := &jw{}
		w.raw("[")
		for i := range n {
			if i > 0 {
				w.raw(",")
			}
			if i == n-1 {
				f2Record(w, i, last)
			} else {
				f2Record(w, i, textOf(plainChunk, per))
			}
		}
		return w.raw("]").b
	})
}

// f3Record is one F3 listing record: about 20 keys and 1.5 KB, no string
// over 300 bytes.
func f3Record(w *jw, i int) {
	w.raw(`{"id":"item-`).int(i).raw(`","sku":"SKU-`).int(700000 + i).raw(`","name":"Listing item number `).int(i).raw(`","brand":"Example Brand","category":"tools/hand-tools",`)
	w.raw(`"price":"19.90","currency":"EUR","stock":`).int(i % 50).raw(`,"available":true,"rating":"4.5","reviews":`).int(i % 300).raw(`,`)
	w.raw(`"url":"https://shop.example.invalid/items/`).int(i).raw(`","image":"https://cdn.example.invalid/img/`).int(i).raw(`.jpg",`)
	w.raw(`"seller":{"id":"s-`).int(i % 9).raw(`","name":"Seller `).int(i % 9).raw(`"},"badges":[{"k":"fast"},{"k":"eco","v":"1"}],"variants":[{"size":"M"},{"size":"L"}],`)
	w.raw(`"snippet":"`).raw(textOf("A sturdy everyday tool with a comfortable grip, suitable for home and workshop use; ships in 2 days. ", 280)).raw(`",`)
	w.raw(`"updated_at":"2026-10-30T10:00:00Z","location":"Berlin, DE","shipping":"Standard (2–4 days), free above 50 EUR, returns within 30 days",`)
	w.raw(`"attributes":[{"k":"material","v":"steel"},{"k":"weight","v":"350 g"},{"k":"length","v":"21 cm"},{"k":"warranty","v":"2 years"}]}`)
}

// f3 is a listing page: a wrapper of 4 keys around about size / 1.5 KB
// records; it is padded with short strings, since it holds no long ones.
func f3(size int) []byte {
	w := &jw{}
	w.raw(`{"items":[`)
	var next jw
	for i := 0; ; i++ {
		next.b = next.b[:0]
		if i > 0 {
			next.raw(",")
		}
		f3Record(&next, i)
		if len(w.b)+len(next.b)+200 > size {
			break
		}
		w.raw(string(next.b))
	}
	w.raw(`],"next_cursor":"c-000042","total":12345,"pad":[`)
	return padStrings(w.b, size, `]}`)
}

// padStrings closes b with strings of at most 250 bytes and close, to
// exactly size bytes (a single byte left over is a space).
func padStrings(b []byte, size int, close string) []byte {
	need := size - len(b) - len(close)
	for need > 252 {
		// A string, a comma, and room for at least an empty string after.
		n := min(250, need-5)
		b = append(b, '"')
		b = append(b, strings.Repeat("x", n)...)
		b = append(b, '"', ',')
		need -= n + 3
	}
	switch {
	case need == 1:
		b = append(b, ' ')
	case need >= 2:
		b = append(b, '"')
		b = append(b, strings.Repeat("x", need-2)...)
		b = append(b, '"')
	}
	b = append(b, close...)
	if len(b) != size {
		panic(fmt.Sprintf("bench: padded to %d bytes, not %d", len(b), size))
	}
	return b
}

// f4 is one wide record merged with metadata: F2's record plus F1's header
// map and small-record array, 30 keys, its text field taking the rest.
func f4(size int) []byte {
	r := rand.New(rand.NewPCG(14, 1))
	var hdr jw
	headerMap(&hdr, 12, r)
	var att jw
	smallRecords(&att, 4)
	return exact(size, plainChunk, func(text string) []byte {
		w := &jw{}
		w.raw(`{"meta":{"run":"r-1","stage":"enrich","version":3},"headers":`).raw(string(hdr.b)).raw(`,"attachments":`).raw(string(att.b)).raw(`,"origin":"import","merged":true,"record":`)
		f2Record(w, 1, text)
		return w.raw("}").b
	})
}

// f5 is an inbound object of 15 keys: short strings, small arrays of 1- or
// 2-key objects and F1's header map; the worst case also carries a markup
// and plain-text pair. The recipient list grows with the size.
func f5(size int) []byte {
	r := rand.New(rand.NewPCG(15, 1))
	var hdr jw
	headerMap(&hdr, 20, r)
	head := func(w *jw, recipients int) {
		w.raw(`{"event":"message.created","id":"evt-0001","received_at":"2026-10-30T09:15:02Z","tenant":"tenant-a",`)
		w.raw(`"source":{"kind":"mail","id":"src-9"},"sender":{"email":"ana@example.invalid","name":"Ana Example"},"subject":"Quarterly report",`)
		w.raw(`"tags":[{"name":"inbound"},{"name":"mail","v":"1"}],"priority":1,"headers":`).raw(string(hdr.b)).raw(`,"recipients":[`)
		for i := range recipients {
			if i > 0 {
				w.raw(",")
			}
			w.raw(`{"email":"person`).int(i).raw(`@example.invalid","name":"Person `).int(i).raw(`"}`)
		}
		w.raw(`]`)
	}
	if size >= 1<<20 {
		markup := textOf(markupChunk, (size-30*kib)*100/127)
		return exact(size, plainChunk, func(text string) []byte {
			w := &jw{}
			head(w, 40)
			w.raw(`,"markup":`).str(markup).raw(`,"text":`).str(text)
			return w.raw("}").b
		})
	}
	var probe jw
	head(&probe, 0)
	n := max(1, (size-len(probe.b)-300)/62)
	w := &jw{}
	head(w, n)
	w.raw(`,"pad":[`)
	return padStrings(w.b, size, `]}`)
}

// f7Item is one extraction item at scale s (1 is the brief's suggested
// item: 300 line records, a 60 KB text dump, a 100 KB markdown string and
// 300 coordinate entries).
func f7Item(w *jw, k int, s float64, r *rand.Rand, last string) {
	lines := max(3, int(300*s))
	w.raw(`{"type":"invoice","page_count":`).int(1 + k%3).raw(`,"data":{"vendor":"Example GmbH","currency":"EUR","items":[`)
	for i := range lines {
		if i > 0 {
			w.raw(",")
		}
		w.raw(`{"line":`).int(i).raw(`,"sku":"P-`).int(1000 + i).raw(`","description":"Item `).int(i).raw(` description","qty":`).int(1 + i%5)
		w.raw(`,"unit":"pcs","price":"`).int(10 + i%90).raw(`.50","tax":"19%","total":"`).int(20 + i%180).raw(`.00","account":"4400","note":null}`)
	}
	w.raw(`]},"text":`).str(textOf(plainChunk, int(60000*s))).raw(`,"markdown":`)
	if last != "" {
		w.str(last)
	} else {
		w.str(textOf(plainChunk, int(100000*s)))
	}
	w.raw(`,"items":[`)
	for i := range lines {
		if i > 0 {
			w.raw(",")
		}
		w.raw(`{"page":1,"x":`).raw(strconv.FormatFloat(r.Float64(), 'f', 4, 64)).raw(`,"y":`).raw(strconv.FormatFloat(r.Float64(), 'f', 4, 64))
		w.raw(`,"w":`).raw(strconv.FormatFloat(r.Float64()/4, 'f', 4, 64)).raw(`,"h":0.0125,"conf":0.98}`)
	}
	w.raw(`]}`)
}

// f7 is an array of extraction items, scaled to size (one small page at
// the typical size, several full items at the worst).
func f7(size int) []byte {
	const item = 260 * kib // the suggested item's size, about
	n := max(1, size/item)
	s := float64(size) / float64(n*item) * 0.9
	return exact(size, plainChunk, func(last string) []byte {
		r := rand.New(rand.NewPCG(17, 1))
		w := &jw{}
		w.raw("[")
		for k := range n {
			if k > 0 {
				w.raw(",")
			}
			l := ""
			if k == n-1 {
				l = last + " "
			}
			f7Item(w, k, s, r, l)
		}
		return w.raw("]").b
	})
}

// f6 is an array of composite documents: each a 25-key stored record with
// a text field, classification arrays, a body string (header block and the
// same text), a full text (the body plus every attachment's text) and two
// extraction items; the suggested worst case is 10 items of about 600 KB.
func f6(size int) []byte {
	const item = 600 * kib
	n := max(1, size/item)
	s := float64(size) / float64(n*item) * 0.85
	return exact(size, plainChunk, func(last string) []byte {
		r := rand.New(rand.NewPCG(16, 1))
		w := &jw{}
		w.raw("[")
		for k := range n {
			if k > 0 {
				w.raw(",")
			}
			text := textOf(plainChunk, int(30000*s))
			w.raw(`{"record":`)
			f2Record(w, k, text)
			w.raw(`,"classes":[{"k":"invoice"}],"topics":[{"k":"billing"},{"k":"q3"}],"entities":[{"k":"org","v":"Example GmbH"}],"flags":[{"k":"reviewed"}],`)
			w.raw(`"body":`).str(`From: Ana Example\nSubject: Quarterly report\n\n` + text).raw(`,"full_text":`)
			if k == n-1 {
				w.str(textOf(plainChunk, int(150000*s)) + last)
			} else {
				w.str(textOf(plainChunk, int(150000*s)))
			}
			w.raw(`,"extractions":[`)
			f7Item(w, 2*k, s/2.6, r, "")
			w.raw(",")
			f7Item(w, 2*k+1, s/2.6, r, "")
			w.raw("]}")
		}
		return w.raw("]").b
	})
}

// f8 is one object with one string: a multipart document whose binary part
// is base64 in lines of 76 characters, line breaks escaped.
func f8(size int) []byte {
	r := rand.New(rand.NewPCG(18, 1))
	head := `MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"b1\"\r\n\r\n--b1\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nSee the attached scan.\r\n` +
		`--b1\r\nContent-Type: application/pdf; name=\"scan.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\n`
	const tail = `\r\n--b1--\r\n`
	body := size - len(`{"raw":""}`) - len(head) - len(tail)
	raw := make([]byte, body/80*57+57)
	for i := range raw {
		raw[i] = byte(r.Uint32())
	}
	enc := base64.StdEncoding.EncodeToString(raw)
	var b strings.Builder
	for i := 0; i < len(enc) && b.Len()+80 <= body; i += 76 {
		b.WriteString(enc[i:min(i+76, len(enc))])
		b.WriteString(`\r\n`)
	}
	lines := b.String()
	lines += strings.Repeat("A", body-len(lines))
	return []byte(`{"raw":"` + head + lines + tail + `"}`)
}
