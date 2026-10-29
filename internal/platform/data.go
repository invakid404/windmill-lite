package platform

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"slices"
)

// The files of a bundle's data/ directory that the local default reads
// (ADR 0010, internal/bundle): each a JSON object keyed by path, with the
// record internal/syncfolder reads from the folder.
const (
	DataResources = "data/resources.json"
	DataVariables = "data/variables.json"
)

// LoadData returns a store holding a bundle's resources and variables, from
// the data/ directory of the bundle tree fsys. A bundle never holds a
// secret variable (ADR 0010): the host supplies those, in a store of its
// own put first in a chain, or through Secrets. A bundle without data/
// gives an empty store.
func LoadData(fsys fs.FS) (*Store, error) {
	s := NewStore()
	var resources map[string]struct {
		ResourceType string          `json:"resource_type"`
		Value        json.RawMessage `json:"value"`
	}
	if err := readData(fsys, DataResources, &resources); err != nil {
		return nil, err
	}
	for _, p := range slices.Sorted(maps.Keys(resources)) {
		r := resources[p]
		if err := s.SetResource(p, Resource{Type: r.ResourceType, Value: r.Value}); err != nil {
			return nil, err
		}
	}
	var variables map[string]struct {
		Value     string `json:"value"`
		IsSecret  bool   `json:"is_secret"`
		Encrypted bool   `json:"encrypted"`
	}
	if err := readData(fsys, DataVariables, &variables); err != nil {
		return nil, err
	}
	for p, v := range variables {
		s.SetVariable(p, Variable{Value: v.Value, IsSecret: v.IsSecret, Encrypted: v.Encrypted})
	}
	return s, nil
}

func readData(fsys fs.FS, name string, v any) error {
	data, err := fs.ReadFile(fsys, name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("platform: %s: %w", name, err)
	}
	return nil
}
