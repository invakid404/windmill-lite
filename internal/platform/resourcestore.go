package platform

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
)

// ResourceStore keeps the resources that steps write through the job API,
// such as the state of the stock client's setState: the entries that createResource, updateResourceValue and
// deleteResource change, which the services read after their providers'
// resources. Memory (MemoryResourceStore) is the default; a host can pass
// any implementation, such as one over a database that runtimes in several
// processes share.
//
// Every implementation keeps this contract, which the exported contract
// suite (wl/storetest) checks:
//
//   - It is safe for concurrent use.
//   - Each operation is atomic and linearizable for one (workspace, path):
//     a Get returns one whole entry, its type and value as one write left
//     them, or none; a write changes the whole entry in one step, so no Get
//     sees part of it.
//   - An entry's Value is kept and returned byte for byte, and the returned
//     bytes are the caller's: changing them changes no entry.
//   - Entries of different workspaces are apart: the same path in two
//     workspaces names two entries.
//   - An error is never reported as false: a store that cannot tell
//     whether an entry exists returns an error.
//   - A write that returns an error may or may not have been applied, as a
//     write over a connection that broke may have been.
//   - A canceled ctx makes an operation return an error.
type ResourceStore interface {
	// Get returns the entry at path in workspace, and false when there is
	// none.
	Get(ctx context.Context, workspace, path string) (Resource, bool, error)
	// Create adds r at path in workspace, and returns false, changing
	// nothing, when the path already holds an entry.
	Create(ctx context.Context, workspace, path string, r Resource) (bool, error)
	// UpdateValue replaces the value of the entry at path in workspace,
	// keeping its type, when that entry has type typ, and returns false,
	// changing and creating nothing, when there is no entry of type typ
	// there.
	//
	// The type is a parameter, checked in the same atomic step as the
	// write, because a host may share the store and write it directly: a
	// caller that checked the type with Get and then wrote could change an
	// entry of another type that the host put there in between. The job
	// API writes only entries of type state.
	UpdateValue(ctx context.Context, workspace, path, typ string, value json.RawMessage) (bool, error)
	// Delete removes the entry at path in workspace when it has type typ,
	// in one atomic step as UpdateValue checks it, and returns false,
	// changing nothing, when there is no entry of type typ there.
	Delete(ctx context.Context, workspace, path, typ string) (bool, error)
}

// MemoryResourceStore is the resource store that keeps its entries in
// memory: one map behind one mutex, whose entries are never changed in
// place, since every write installs a new entry with its own copy of the
// bytes, and whose reads return copies. Its entries last as long as the
// value; a runtime that makes its own loses them at Close (deviation D24
// of ADR 0001). The zero value is not usable: NewMemoryResourceStore makes
// one.
type MemoryResourceStore struct {
	mu      sync.Mutex
	entries map[storeKey]Resource
}

type storeKey struct{ workspace, path string }

// NewMemoryResourceStore returns an empty memory store.
func NewMemoryResourceStore() *MemoryResourceStore {
	return &MemoryResourceStore{entries: map[storeKey]Resource{}}
}

// Get returns a copy of the entry at path in workspace.
func (m *MemoryResourceStore) Get(ctx context.Context, workspace, path string) (Resource, bool, error) {
	if err := ctx.Err(); err != nil {
		return Resource{}, false, err
	}
	m.mu.Lock()
	r, ok := m.entries[storeKey{workspace, path}]
	m.mu.Unlock()
	if !ok {
		return Resource{}, false, nil
	}
	// The entry is never changed in place, so a copy made after the lock
	// is released is still the one whole value this Get read.
	return Resource{Type: r.Type, Value: slices.Clone(r.Value)}, true, nil
}

// Create adds a copy of r at path in workspace unless the path holds an
// entry.
func (m *MemoryResourceStore) Create(ctx context.Context, workspace, path string, r Resource) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	e := Resource{Type: r.Type, Value: slices.Clone(r.Value)}
	k := storeKey{workspace, path}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries[k]; ok {
		return false, nil
	}
	m.entries[k] = e
	return true, nil
}

// UpdateValue installs a new entry with the old entry's type and a copy of
// value, when the entry at path in workspace has type typ; the check and the
// write share one hold of the lock.
func (m *MemoryResourceStore) UpdateValue(ctx context.Context, workspace, path, typ string, value json.RawMessage) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	v := slices.Clone(value)
	k := storeKey{workspace, path}
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.entries[k]
	if !ok || old.Type != typ {
		return false, nil
	}
	m.entries[k] = Resource{Type: old.Type, Value: v}
	return true, nil
}

// Delete removes the entry at path in workspace when it has type typ, under
// the same hold of the lock as the check.
func (m *MemoryResourceStore) Delete(ctx context.Context, workspace, path, typ string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	k := storeKey{workspace, path}
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.entries[k]; !ok || old.Type != typ {
		return false, nil
	}
	delete(m.entries, k)
	return true, nil
}
