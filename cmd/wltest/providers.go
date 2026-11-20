package main

import (
	"errors"

	"github.com/invakid404/windmill-lite/internal/conformance"
	"github.com/invakid404/windmill-lite/internal/platform"
	"github.com/invakid404/windmill-lite/internal/syncfolder"
	"github.com/invakid404/windmill-lite/wl"
)

// platformOf returns platform services that hold the resources and
// variables of folder, when it is not nil, then those of setup, which the
// oracle creates after the push (OR-31), so they replace the folder's. A
// setup applied before the push is removed by the push, so it holds nothing
// at run time. A secret is held as the folder holds it: ciphertext without
// plainSecrets, which needs a host secret resolver; a setup's secret is its
// plaintext, which wltest passes in as a host passes its secrets, never
// through a bundle (ADR 0010). Its object store is a directory of its own,
// objects; its job registry and flow user state are the services' own, and
// it has no dispatcher. Its caller is the step checker (runCase), with no
// folder and the case's setup; a flow fixture's runtime takes its host data
// from hostDataOf instead.
func platformOf(folder *syncfolder.Folder, setup *conformance.Setup, policy, objects string) (*platform.Services, error) {
	store := platform.NewStore()
	if folder != nil {
		for _, p := range folder.Paths(syncfolder.KindResource) {
			r, err := folder.Resource(p)
			if err != nil {
				return nil, err
			}
			if err := store.SetResource(p, platform.Resource{Type: r.ResourceType, Value: r.Value}); err != nil {
				return nil, err
			}
		}
		for _, p := range folder.Paths(syncfolder.KindVariable) {
			v, err := folder.Variable(p)
			var se *syncfolder.Error
			switch {
			case errors.As(err, &se) && se.Kind == syncfolder.ErrFiltered:
				continue
			case err != nil:
				return nil, err
			}
			store.SetVariable(p, platform.Variable{Value: v.Value, IsSecret: v.IsSecret, Encrypted: v.Encrypted})
		}
	}
	if setup != nil && policy != conformance.SetupBeforePush {
		for _, v := range setup.Variables {
			store.SetVariable(v.Path, platform.Variable{Value: v.Value, IsSecret: v.IsSecret})
		}
		for _, r := range setup.Resources {
			if err := store.SetResource(r.Path, platform.Resource{Type: r.ResourceType, Value: r.Value}); err != nil {
				return nil, err
			}
		}
	}
	dir, err := platform.NewDirObjects(objects)
	if err != nil {
		return nil, err
	}
	return &platform.Services{Resources: store, Variables: store, Objects: dir}, nil
}

// hostDataOf returns what a flow fixture's runtime is opened with as the
// host's data (wl.Options), which replaces the bundle's data/ (ADR 0007):
// the secrets of its sync folder, which a bundle never holds (ADR 0010),
// as the folder holds them (ciphertext without plainSecrets), then the
// variables and resources of its setup, as platformOf reads them.
func hostDataOf(folder *syncfolder.Folder, setup *conformance.Setup, policy string) (map[string]wl.Variable, map[string]wl.Resource, error) {
	variables := map[string]wl.Variable{}
	resources := map[string]wl.Resource{}
	for _, p := range folder.Paths(syncfolder.KindVariable) {
		v, err := folder.Variable(p)
		var se *syncfolder.Error
		switch {
		case errors.As(err, &se) && se.Kind == syncfolder.ErrFiltered:
			continue
		case err != nil:
			return nil, nil, err
		case v.IsSecret:
			variables[p] = wl.Variable{Value: v.Value, Secret: true, Ciphertext: v.Encrypted}
		}
	}
	if setup != nil && policy != conformance.SetupBeforePush {
		for _, v := range setup.Variables {
			variables[v.Path] = wl.Variable{Value: v.Value, Secret: v.IsSecret}
		}
		for _, r := range setup.Resources {
			resources[r.Path] = wl.Resource{Type: r.ResourceType, Value: r.Value}
		}
	}
	return variables, resources, nil
}
