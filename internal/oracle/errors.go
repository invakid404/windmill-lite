package oracle

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
)

// Refusal is a run the harness refuses to start or continue, in the JSON
// form the reference gives it, for example
// {"error":"oracle_version_mismatch","got":"EE v1.767.0"} (OR-1/b). Its
// Error text is that JSON.
type Refusal struct {
	// Code is the "error" member.
	Code string
	// Fields are the other members, in order, as JSON text.
	Fields []RefusalField
}

// RefusalField is one member of a refusal.
type RefusalField struct {
	Name  string
	Value string // JSON text
}

// JSON returns the refusal as one JSON object.
func (r *Refusal) JSON() []byte {
	var b bytes.Buffer
	b.WriteString(`{"error":`)
	b.WriteString(jsonString(r.Code))
	for _, f := range r.Fields {
		b.WriteByte(',')
		b.WriteString(jsonString(f.Name))
		b.WriteByte(':')
		b.WriteString(f.Value)
	}
	b.WriteByte('}')
	return b.Bytes()
}

func (r *Refusal) Error() string { return string(r.JSON()) }

func jsonString(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

func refusal(code string, kv ...string) *Refusal {
	r := &Refusal{Code: code}
	for i := 0; i+1 < len(kv); i += 2 {
		r.Fields = append(r.Fields, RefusalField{Name: kv[i], Value: kv[i+1]})
	}
	return r
}

// ErrVersionMismatch is the refusal of an oracle whose /api/version is not
// exactly OracleVersion (OR-1/b).
func ErrVersionMismatch(got string) *Refusal {
	return refusal("oracle_version_mismatch", "got", jsonString(got))
}

// ErrImageMismatch is the refusal of an image whose RepoDigests do not
// carry the pinned digest (OR-1, OR-V5/b).
func ErrImageMismatch(image string, repoDigests []string) *Refusal {
	ds, _ := json.Marshal(repoDigests)
	return refusal("oracle_image_mismatch", "image", jsonString(image), "repo_digests", string(ds))
}

// ErrRequiresWorkers is the refusal of a fixture that needs more workers
// than the session has, instead of a run that would hang (OR-5/a).
func ErrRequiresWorkers(need, have int) *Refusal {
	return refusal("fixture_requires_workers", "need", strconv.Itoa(need), "have", strconv.Itoa(have))
}

// ErrForeignEffect is what the effect sink answers to an effect whose root
// is not the run being recorded (OR-16/c).
func ErrForeignEffect() *Refusal { return refusal("foreign_effect") }

// ErrInsufficientDisk is the refusal of a pull that would leave less than
// the reserve (OR-30/a).
func ErrInsufficientDisk(free, need, reserve uint64) *Refusal {
	return refusal("insufficient_disk", "free_gib", gib(free), "need_gib", gib(need), "reserve_gib", gibInt(reserve))
}

// ErrDiskFloor is the abort of a pull or a run when free space falls below
// the floor (OR-30 steps 4 and 7). There is no automatic resume.
func ErrDiskFloor(phase string, free, floor uint64) *Refusal {
	return refusal("disk_floor_reached", "phase", jsonString(phase), "free_gib", gib(free), "floor_gib", gibInt(floor))
}

// ErrDiskAfterPull is the refusal to start the oracle when settled free
// space after a completed pull is below the floor; the harness removes the
// images it pulled (OR-30 step 6).
func ErrDiskAfterPull(free, floor uint64, removed []string) *Refusal {
	rs, _ := json.Marshal(removed)
	return refusal("insufficient_disk_after_pull", "free_gib", gib(free), "floor_gib", gibInt(floor), "removed", string(rs))
}

// gib writes a measured byte count in GiB with two decimals at most and
// one at least (9 GiB is 9.0, as OR-30/a writes it).
func gib(n uint64) string {
	v := float64(n) / float64(GiB)
	s := strconv.FormatFloat(float64(int64(v*100+0.5))/100, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// gibInt writes a policy constant in whole GiB.
func gibInt(n uint64) string { return strconv.FormatUint(n/GiB, 10) }
