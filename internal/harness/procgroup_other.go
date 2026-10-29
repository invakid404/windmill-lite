//go:build !linux && !darwin

package harness

// groupMembersOf reports, where the engine cannot list a group's members,
// one member standing for any group that has a process, so a child group's
// ownership is its group's existence only.
func groupMembersOf(pgids []int) map[int][]groupMember {
	out := map[int][]groupMember{}
	for _, g := range pgids {
		if groupAlive(g) {
			out[g] = []groupMember{{pid: g, start: 1}}
		}
	}
	return out
}

// processHasGuard reports no guardian where the engine cannot read a
// process's arguments: ownership falls back to the members (continues).
func processHasGuard(int, string) bool { return false }

// procInfoOf reads nothing where the engine cannot read another process:
// it reaps no member of a group (reapMember).
func procInfoOf(int) (procInfo, bool) { return procInfo{}, false }
