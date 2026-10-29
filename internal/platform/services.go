package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// Resource is a resource as the services hold it (SY-10): its type, and
// its value as the JSON the workspace holds. $var: and $res: strings in
// the value are kept; the walker resolves them when it is read.
type Resource struct {
	Type  string
	Value json.RawMessage
}

// Variable is a variable as the services hold it (SY-11). A secret's Value
// is its plaintext unless Encrypted says it is ciphertext under the
// workspace key, as a sync folder pulled without plainSecrets holds it,
// which only a host secret resolver can turn into plaintext.
type Variable struct {
	Value     string
	IsSecret  bool
	Encrypted bool
}

// Resources is the resource service of ADR 0007: the resources a run can
// read, by path. A host replaces it, or puts its own in front of the
// local default (Chain).
type Resources interface {
	// Resource returns the resource at path, and false when there is none.
	Resource(ctx context.Context, path string) (Resource, bool, error)
}

// Variables is the variable service of ADR 0007: the variables a run can
// read, by path, secrets included, whose plaintext Secrets reveals.
type Variables interface {
	// Variable returns the variable at path, and false when there is none.
	Variable(ctx context.Context, path string) (Variable, bool, error)
}

// VariableUpdater is a variable service that also takes writes: the
// update of an existing variable's value through the job API (WA-11,
// milestone F1). A variable service that does not implement it is
// read-only, and the facade answers its update route 501.
type VariableUpdater interface {
	// UpdateVariable sets the value of the variable at path when value is
	// not nil, and its secrecy when secret is not nil, and reports false
	// when there is no such variable.
	UpdateVariable(ctx context.Context, path string, value *string, secret *bool) (bool, error)
}

// Secrets is the secret service of ADR 0007: it reveals the plaintext of a
// secret variable, and decrypts the $encrypted: values of worker argument
// resolution (WR-5). windmill-lite never decrypts Windmill ciphertext
// itself (JE-43, SY-11): the default, LocalSecrets, reveals a secret held
// as plaintext and fails with ErrSecretUnavailable for ciphertext.
type Secrets interface {
	// Reveal returns the plaintext of the secret variable at path.
	Reveal(ctx context.Context, path string, v Variable) (string, error)
	// Decrypt returns the plaintext of an $encrypted: value, which the
	// worker then parses as JSON.
	Decrypt(ctx context.Context, ciphertext string) (string, error)
}

// ValueResolver is the host secret layer of ADR 0007: after interpolation,
// each resource value and each secret variable's plaintext passes through
// the host's chain of resolvers, which may recognise markers of the
// host's own inside values (a $secret: prefix, say) and replace them.
// windmill-lite defines no marker of its own.
type ValueResolver interface {
	// ResolveValue returns the value to use in place of v, the value of
	// the resource at path (resource true) or the plaintext of the secret
	// variable at path, as a JSON string (resource false).
	ResolveValue(ctx context.Context, path string, resource bool, v jsonval.Value) (jsonval.Value, error)
}

// ErrSecretUnavailable is the failure of a secret that no host resolver
// supplies: ciphertext under the workspace key, or an $encrypted: value
// (ADR 0007, secret_unavailable).
var ErrSecretUnavailable = errors.New("secret_unavailable")

// LocalSecrets is the default secret service: a secret held as plaintext
// (a sync folder pulled with plainSecrets, or one a host passed in) is
// revealed as it is; ciphertext and $encrypted: values are unavailable.
type LocalSecrets struct{}

func (LocalSecrets) Reveal(_ context.Context, path string, v Variable) (string, error) {
	if v.Encrypted {
		return "", fmt.Errorf("%w: the secret variable %s is ciphertext under the workspace key, which windmill-lite never decrypts, and no host secret resolver supplies it", ErrSecretUnavailable, path)
	}
	return v.Value, nil
}

func (LocalSecrets) Decrypt(context.Context, string) (string, error) {
	return "", fmt.Errorf("%w: an $encrypted: value is ciphertext under the workspace key, which windmill-lite never decrypts, and no host secret resolver supplies it", ErrSecretUnavailable)
}

// Store is the local default of the resource and variable services: the
// resources and variables of a bundle (LoadData) or of a sync folder, and
// those a host adds. It is safe for concurrent use.
type Store struct {
	mu        sync.RWMutex
	resources map[string]Resource
	variables map[string]Variable
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{resources: map[string]Resource{}, variables: map[string]Variable{}}
}

// SetResource adds the resource at path, or replaces it. Its value must be
// a JSON text that the workspace could store: jsonb refuses a NUL escape,
// an unpaired surrogate escape and a number beyond numeric's range
// (FX-48, FX-51), and Windmill refuses to create such a resource.
func (s *Store) SetResource(path string, r Resource) error {
	if _, err := jsonval.FromProducer(r.Value).Stored(); err != nil {
		return fmt.Errorf("platform: resource %s: the value cannot be stored: %w", path, err)
	}
	r.Value = slices.Clone(r.Value)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resources[path] = r
	return nil
}

// SetVariable adds the variable at path, or replaces it.
func (s *Store) SetVariable(path string, v Variable) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.variables[path] = v
}

// UpdateVariable sets the value of the variable at path when value is not
// nil, and its secrecy when secret is not nil (VariableUpdater). A value
// set is plaintext: a secret held as ciphertext stops being so.
func (s *Store) UpdateVariable(_ context.Context, path string, value *string, secret *bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.variables[path]
	if !ok {
		return false, nil
	}
	if value != nil {
		v.Value, v.Encrypted = *value, false
	}
	if secret != nil {
		v.IsSecret = *secret
	}
	s.variables[path] = v
	return true, nil
}

// Resource returns the resource at path.
func (s *Store) Resource(_ context.Context, path string) (Resource, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.resources[path]
	return r, ok, nil
}

// Variable returns the variable at path.
func (s *Store) Variable(_ context.Context, path string) (Variable, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.variables[path]
	return v, ok, nil
}

// Paths returns the paths of the store's resources and variables, sorted.
func (s *Store) Paths() (resources, variables []string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Sorted(maps.Keys(s.resources)), slices.Sorted(maps.Keys(s.variables))
}

// ResourceChain is a chain of resource services: the first that has a
// path answers for it, so a host's overrides come before the bundle's
// resources (WA-40's design note).
type ResourceChain []Resources

func (c ResourceChain) Resource(ctx context.Context, path string) (Resource, bool, error) {
	for _, r := range c {
		v, ok, err := r.Resource(ctx, path)
		if err != nil || ok {
			return v, ok, err
		}
	}
	return Resource{}, false, nil
}

// VariableChain is a chain of variable services, as ResourceChain.
type VariableChain []Variables

func (c VariableChain) Variable(ctx context.Context, path string) (Variable, bool, error) {
	for _, r := range c {
		v, ok, err := r.Variable(ctx, path)
		if err != nil || ok {
			return v, ok, err
		}
	}
	return Variable{}, false, nil
}

// UpdateVariable updates the variable at path in the first service of the
// chain that has it, the one a read answers from (WA-11). That service
// must be a VariableUpdater; otherwise the variable is read-only
// (ErrReadOnlyVariables).
func (c VariableChain) UpdateVariable(ctx context.Context, path string, value *string, secret *bool) (bool, error) {
	for _, r := range c {
		_, ok, err := r.Variable(ctx, path)
		if err != nil {
			return false, err
		}
		if !ok {
			continue
		}
		u, can := r.(VariableUpdater)
		if !can {
			return false, ErrReadOnlyVariables
		}
		return u.UpdateVariable(ctx, path, value, secret)
	}
	return false, nil
}

// Providers are the platform providers a run may choose for itself (H13,
// ADR 0008): its resources, variables, secrets and value resolvers, for
// example a resource resolver per tenant. A nil field keeps the services'.
type Providers struct {
	Resources Resources
	Variables Variables
	Secrets   Secrets
	Resolvers []ValueResolver
}

// WithProviders returns services that answer with p's providers in place of
// s's, where p sets them, and share s's job registry, flow user state,
// object store, dispatcher and result cache: a run that chose its own
// providers (H13) is still one of the runtime's runs, whose jobs s's
// registry knows. Fields of s set later are not seen.
func (s *Services) WithProviders(p Providers) *Services {
	if s == nil {
		s = &Services{}
	}
	v := &Services{
		Resources: s.Resources, Variables: s.Variables, Secrets: s.Secrets, Resolvers: s.Resolvers,
		Jobs: s.jobs(), UserState: s.userState(), Objects: s.Objects, Dispatcher: s.Dispatcher, Results: s.ResultCache(),
	}
	if p.Resources != nil {
		v.Resources = p.Resources
	}
	if p.Variables != nil {
		v.Variables = p.Variables
	}
	if p.Secrets != nil {
		v.Secrets = p.Secrets
	}
	if p.Resolvers != nil {
		v.Resolvers = p.Resolvers
	}
	return v
}

// Services are the platform services of ADR 0007: the resources,
// variables and secrets of milestone PF2, with the host's value resolvers,
// and the job registry, flow user state, object store and dispatcher of
// milestone PF3, and the result cache of milestone N4, which only the
// engine uses. They are what the facade answers its routes from, and
// what the engine resolves a leaf's arguments, flow_env and the
// expression helpers through and registers its runs with; a host gives
// both the same *Services. A nil field takes its default: no resources,
// no variables, LocalSecrets, no resolvers; for Jobs one Registry per
// Services, and for UserState one UserStates per Services over whichever
// Jobs it holds, each made when first needed, so the facade and the
// engine that share a *Services share them; no object store, whose calls
// answer 501; no dispatcher, so that no target is in the bundle; and for
// Results one MemoryResults per Services, made when first needed. A nil
// *Services has no registry either: it keeps no job. Services are
// safe for concurrent use when their services are; a field is set before
// any run starts, and never after.
type Services struct {
	Resources Resources
	Variables Variables
	Secrets   Secrets
	Resolvers []ValueResolver

	Jobs       Jobs
	UserState  FlowUserState
	Objects    ObjectStore
	Dispatcher Dispatcher

	Results ResultCache

	localOnce   sync.Once
	local       *Registry
	statesOnce  sync.Once
	states      *UserStates
	resultsOnce sync.Once
	results     *MemoryResults
}

// ResultCache returns the result cache the engine keeps cache_ttl results
// in (FX-40): Results, or the services' own MemoryResults, made when first
// needed, so the runs of every engine that shares the *Services share it;
// nil for a nil *Services.
func (s *Services) ResultCache() ResultCache {
	switch {
	case s == nil:
		return nil
	case s.Results != nil:
		return s.Results
	}
	s.resultsOnce.Do(func() { s.results = NewMemoryResults() })
	return s.results
}

// Registry returns the services' own registry, the default of Jobs,
// making it when first needed; nil for a nil *Services.
func (s *Services) Registry() *Registry {
	if s == nil {
		return nil
	}
	s.localOnce.Do(func() { s.local = NewRegistry() })
	return s.local
}

// JobRegistry returns the job registry the engine and the facade register
// their jobs with: Jobs, or the services' own registry. When the services
// hold no FlowUserState of their own, a root's registration through it
// also makes the default user state service forget the root's maps once
// the registry no longer reports the root live.
func (s *Services) JobRegistry() Jobs {
	if s == nil {
		return noJobs{}
	}
	return servicesJobs{Jobs: s.jobs(), states: s.defaultStates()}
}

func (s *Services) jobs() Jobs {
	switch {
	case s == nil:
		return noJobs{}
	case s.Jobs != nil:
		return s.Jobs
	}
	return s.Registry()
}

// defaultStates is the services' own UserStates, over their Jobs, or nil
// when they hold a FlowUserState of their own.
func (s *Services) defaultStates() *UserStates {
	if s == nil || s.UserState != nil {
		return nil
	}
	s.statesOnce.Do(func() { s.states = NewUserStates(s.jobs()) })
	return s.states
}

func (s *Services) userState() FlowUserState {
	switch {
	case s == nil:
		return NewUserStates(noJobs{})
	case s.UserState != nil:
		return s.UserState
	}
	return s.defaultStates()
}

func (s *Services) resource(ctx context.Context, path string) (Resource, bool, error) {
	if s == nil || s.Resources == nil {
		return Resource{}, false, nil
	}
	return s.Resources.Resource(ctx, path)
}

func (s *Services) variable(ctx context.Context, path string) (Variable, bool, error) {
	if s == nil || s.Variables == nil {
		return Variable{}, false, nil
	}
	return s.Variables.Variable(ctx, path)
}

func (s *Services) secrets() Secrets {
	if s == nil || s.Secrets == nil {
		return LocalSecrets{}
	}
	return s.Secrets
}

func (s *Services) resolvers() []ValueResolver {
	if s == nil {
		return nil
	}
	return s.Resolvers
}
