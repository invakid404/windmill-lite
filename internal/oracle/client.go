package oracle

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/invakid404/windmill-lite/internal/conformance"
)

// Client calls the oracle's REST API: the endpoints of the Apache-2.0
// backend/windmill-api/openapi.yaml at v1.767.0, vendored in openapi/. A
// test checks that every operation it calls is in that file. Arguments and
// results stay raw JSON text, because their member order and number
// spelling are observable (OR-13).
type Client struct {
	// BaseURL is the oracle's base URL, without /api.
	BaseURL string
	// Token is the bearer token; empty before login.
	Token string
	// HTTP is the HTTP client; nil means http.DefaultClient.
	HTTP *http.Client
	// Capture, when set, records every exchange (for replay tests).
	Capture *Capture
}

// APIError is a response with an unexpected status.
type APIError struct {
	Method, Path string
	Status       int
	Body         string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("oracle: %s %s: HTTP %d: %s", e.Method, e.Path, e.Status, strings.TrimSpace(e.Body))
}

// Capture records the exchanges of a client, keyed by method and path with
// query. The last response for a key wins, so a replay of a walk sees the
// completed state.
type Capture struct {
	mu        sync.Mutex
	Exchanges []Exchange `json:"exchanges"`
}

// Exchange is one recorded request and its response.
type Exchange struct {
	Method      string `json:"method"`
	URL         string `json:"url"`
	Status      int    `json:"status"`
	ContentType string `json:"content_type"`
	// Encoding is empty when Body is the body's text, or
	// conformance.BodyBase64 when the body is not UTF-8 and Body is its
	// base64 text, so a capture keeps every byte.
	Encoding string `json:"encoding,omitempty"`
	Body     string `json:"body"`
}

// exchangeBody returns the Body and Encoding that keep data losslessly.
func exchangeBody(data []byte) (body, encoding string) {
	if utf8.Valid(data) {
		return string(data), ""
	}
	return base64.StdEncoding.EncodeToString(data), conformance.BodyBase64
}

// Bytes returns the exchange's body bytes.
func (e Exchange) Bytes() ([]byte, error) {
	return conformance.Response{Encoding: e.Encoding, Body: e.Body}.Bytes()
}

func (c *Capture) add(e Exchange) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Exchanges = append(c.Exchanges, e)
}

// Handler serves the captured responses: for each method and URL, the last
// one recorded. Anything else is 404.
func (c *Capture) Handler() http.Handler {
	last := make(map[string]Exchange)
	for _, e := range c.Exchanges {
		last[e.Method+" "+e.URL] = e
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e, ok := last[r.Method+" "+r.URL.RequestURI()]
		if !ok {
			http.Error(w, "not captured: "+r.Method+" "+r.URL.RequestURI(), http.StatusNotFound)
			return
		}
		body, err := e.Bytes()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", e.ContentType)
		w.WriteHeader(e.Status)
		_, _ = w.Write(body)
	})
}

// do sends one request and returns the body of a response whose status is
// in want.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, want ...int) ([]byte, error) {
	resp, err := c.send(ctx, method, path, query, body)
	if err != nil {
		return nil, err
	}
	for _, w := range want {
		if resp.Status == w {
			return resp.Body, nil
		}
	}
	return resp.Body, &APIError{Method: method, Path: path, Status: resp.Status, Body: string(resp.Body)}
}

// HTTPResponse is a response whatever its status: what run-and-wait
// answers is the observation, not a failure of the harness (FX-36).
type HTTPResponse struct {
	Status      int
	ContentType string
	Body        []byte
}

// send sends one request and returns its response, recording the exchange
// when the client captures.
func (c *Client) send(ctx context.Context, method, path string, query url.Values, body any) (HTTPResponse, error) {
	return c.sendRedirects(ctx, method, path, query, body, true)
}

// sendRedirects is send; with follow false a redirect response is returned
// as it is, not followed, through a copy of the client's HTTP client.
func (c *Client) sendRedirects(ctx context.Context, method, path string, query url.Values, body any, follow bool) (HTTPResponse, error) {
	u := strings.TrimSuffix(c.BaseURL, "/") + "/api" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		switch b := body.(type) {
		case json.RawMessage:
			rd = bytes.NewReader(b)
		default:
			data, err := json.Marshal(b)
			if err != nil {
				return HTTPResponse{}, err
			}
			rd = bytes.NewReader(data)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return HTTPResponse{}, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	h := c.HTTP
	if h == nil {
		h = http.DefaultClient
	}
	if !follow {
		cp := *h
		cp.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		h = &cp
	}
	resp, err := h.Do(req)
	if err != nil {
		return HTTPResponse{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return HTTPResponse{}, err
	}
	out := HTTPResponse{Status: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: data}
	if c.Capture != nil {
		b, enc := exchangeBody(data)
		c.Capture.add(Exchange{Method: method, URL: req.URL.RequestURI(), Status: out.Status, ContentType: out.ContentType, Encoding: enc, Body: b})
	}
	return out, nil
}

func (c *Client) getJSON(ctx context.Context, path string, query url.Values, v any) error {
	data, err := c.do(ctx, http.MethodGet, path, query, nil, http.StatusOK)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("oracle: GET %s: %w", path, err)
	}
	return nil
}

// ws returns the path prefix of a workspace.
func ws(workspace string) string { return "/w/" + url.PathEscape(workspace) }

// itemPath escapes each segment of a Windmill item path (f/folder/name)
// and keeps the slashes, as the API's {path} parameters expect.
func itemPath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// Version returns GET /version, the plain-text version string (OR-1).
func (c *Client) Version(ctx context.Context) (string, error) {
	data, err := c.do(ctx, http.MethodGet, "/version", nil, nil, http.StatusOK)
	return strings.TrimSpace(string(data)), err
}

// Health is GET /health/status.
type Health struct {
	Status          string `json:"status"`
	DatabaseHealthy bool   `json:"database_healthy"`
	WorkersAlive    int    `json:"workers_alive"`
}

// Health returns GET /health/status?force=true; the endpoint caches for
// 5 s otherwise (OR-6). A 503 still carries the status.
func (c *Client) Health(ctx context.Context) (Health, error) {
	var h Health
	data, err := c.do(ctx, http.MethodGet, "/health/status", url.Values{"force": {"true"}}, nil, http.StatusOK, http.StatusServiceUnavailable)
	if err != nil {
		return h, err
	}
	return h, json.Unmarshal(data, &h)
}

// Login returns the plain-text session token of POST /auth/login (OR-6).
func (c *Client) Login(ctx context.Context, email, password string) (string, error) {
	data, err := c.do(ctx, http.MethodPost, "/auth/login", nil, map[string]string{"email": email, "password": password}, http.StatusOK)
	return strings.TrimSpace(string(data)), err
}

// CreateWorkspace creates a workspace whose id and name are id (OR-6).
func (c *Client) CreateWorkspace(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodPost, "/workspaces/create", nil, map[string]string{"id": id, "name": id}, http.StatusOK, http.StatusCreated)
	return err
}

// DeleteWorkspace deletes a workspace, DELETE /workspaces/delete/{id}, as
// the superadmin. CE allows only two workspaces besides admins, so a
// session frees each fixture's workspace after its runs.
func (c *Client) DeleteWorkspace(ctx context.Context, id string) error {
	_, err := c.do(ctx, http.MethodDelete, "/workspaces/delete/"+url.PathEscape(id), nil, nil, http.StatusOK)
	return err
}

// CreateFolder creates the folder name, POST /w/{ws}/folders/create.
func (c *Client) CreateFolder(ctx context.Context, workspace, name string) error {
	_, err := c.do(ctx, http.MethodPost, ws(workspace)+"/folders/create", nil, map[string]string{"name": name}, http.StatusOK, http.StatusCreated)
	return err
}

// NewScript is the body of POST /w/{ws}/scripts/create: the fields an API
// deployment sets (NewScript in the vendored schema).
type NewScript struct {
	Path        string `json:"path"`
	Summary     string `json:"summary"`
	Description string `json:"description"`
	Content     string `json:"content"`
	Language    string `json:"language"`
	CacheTTL    *int   `json:"cache_ttl,omitempty"`
}

// CreateScript creates a script and returns the hash Windmill assigned it,
// which the endpoint answers as text.
func (c *Client) CreateScript(ctx context.Context, workspace string, s NewScript) (string, error) {
	b, err := c.do(ctx, http.MethodPost, ws(workspace)+"/scripts/create", nil, s, http.StatusOK, http.StatusCreated)
	return strings.TrimSpace(string(b)), err
}

// CreateFlow creates a flow, POST /w/{ws}/flows/create. body is the JSON
// object the endpoint takes (path, summary, description, value, schema),
// sent as it is, so its member order and number text reach the server.
func (c *Client) CreateFlow(ctx context.Context, workspace string, body json.RawMessage) error {
	_, err := c.do(ctx, http.MethodPost, ws(workspace)+"/flows/create", nil, body, http.StatusOK, http.StatusCreated)
	return err
}

// QueueCount returns database_length of GET /w/{ws}/jobs/queue/count.
func (c *Client) QueueCount(ctx context.Context, workspace string) (int, error) {
	var v struct {
		DatabaseLength int `json:"database_length"`
	}
	err := c.getJSON(ctx, ws(workspace)+"/jobs/queue/count", nil, &v)
	return v.DatabaseLength, err
}

// ScriptRef is a deployed script.
type ScriptRef struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
}

// ListScripts lists the workspace's scripts.
func (c *Client) ListScripts(ctx context.Context, workspace string) ([]ScriptRef, error) {
	var v []ScriptRef
	err := c.getJSON(ctx, ws(workspace)+"/scripts/list", url.Values{"per_page": {"1000"}}, &v)
	return v, err
}

// ListFlows lists the paths of the workspace's flows.
func (c *Client) ListFlows(ctx context.Context, workspace string) ([]string, error) {
	var v []struct {
		Path string `json:"path"`
	}
	if err := c.getJSON(ctx, ws(workspace)+"/flows/list", url.Values{"per_page": {"1000"}}, &v); err != nil {
		return nil, err
	}
	paths := make([]string, len(v))
	for i, f := range v {
		paths[i] = f.Path
	}
	return paths, nil
}

// DeploymentStatus is a script's or flow's deployment status (OR-9).
type DeploymentStatus struct {
	Lock          *string `json:"lock"`
	LockErrorLogs *string `json:"lock_error_logs"`
	JobID         *string `json:"job_id"`
}

// ScriptDeploymentStatus returns GET /w/{ws}/scripts/deployment_status/h/{hash}.
func (c *Client) ScriptDeploymentStatus(ctx context.Context, workspace, hash string) (DeploymentStatus, error) {
	var v DeploymentStatus
	err := c.getJSON(ctx, ws(workspace)+"/scripts/deployment_status/h/"+url.PathEscape(hash), nil, &v)
	return v, err
}

// FlowDeploymentStatus returns GET /w/{ws}/flows/deployment_status/p/{path}.
func (c *Client) FlowDeploymentStatus(ctx context.Context, workspace, path string) (DeploymentStatus, error) {
	var v DeploymentStatus
	err := c.getJSON(ctx, ws(workspace)+"/flows/deployment_status/p/"+itemPath(path), nil, &v)
	return v, err
}

// GetFlow returns the deployed flow of GET /w/{ws}/flows/get/{path}.
func (c *Client) GetFlow(ctx context.Context, workspace, path string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, ws(workspace)+"/flows/get/"+itemPath(path), nil, nil, http.StatusOK)
}

// ResultMaybe is GET /w/{ws}/jobs_u/completed/get_result_maybe/{id}: the
// full stored result once the job completed (OR-10, OR-V1). Its success is
// a completion signal, not the status model (OR-V2).
type ResultMaybe struct {
	Completed bool            `json:"completed"`
	Started   *bool           `json:"started"`
	Success   *bool           `json:"success"`
	Result    json.RawMessage `json:"result"`
}

// ResultMaybe polls a job once.
func (c *Client) ResultMaybe(ctx context.Context, workspace, id string) (ResultMaybe, error) {
	var v ResultMaybe
	err := c.getJSON(ctx, ws(workspace)+"/jobs_u/completed/get_result_maybe/"+url.PathEscape(id), url.Values{"get_started": {"true"}}, &v)
	return v, err
}

// RunFlow submits a flow by path asynchronously and returns the job id
// from the plain-text 201 response (OR-10). jobID pre-assigns the root id,
// so the effect sink knows the run before any step can post to it.
func (c *Client) RunFlow(ctx context.Context, workspace, path string, args json.RawMessage, jobID string) (string, error) {
	return c.run(ctx, ws(workspace)+"/jobs/run/f/"+itemPath(path), args, jobID)
}

// RunScript submits a script by path asynchronously (OR-10).
func (c *Client) RunScript(ctx context.Context, workspace, path string, args json.RawMessage, jobID string) (string, error) {
	return c.run(ctx, ws(workspace)+"/jobs/run/p/"+itemPath(path), args, jobID)
}

// RunWaitFlow submits a flow by path through the synchronous run-and-wait
// endpoint and returns its HTTP response, whatever the status (FX-36,
// FX-37, OR-10/b). The harness observes runs through RunFlow and the poll
// (OR-10); this call exists to record the response itself, so a redirect
// that response metadata asks for (a 3xx status with a Location header) is
// returned, never followed. jobID pre-assigns the root id, as for RunFlow,
// so the run can be walked and its effects kept.
func (c *Client) RunWaitFlow(ctx context.Context, workspace, path string, args json.RawMessage, jobID string) (HTTPResponse, error) {
	q := url.Values{"job_id": {jobID}}
	return c.sendRedirects(ctx, http.MethodPost, ws(workspace)+"/jobs/run_wait_result/f/"+itemPath(path), q, args, false)
}

func (c *Client) run(ctx context.Context, path string, args json.RawMessage, jobID string) (string, error) {
	var q url.Values
	if jobID != "" {
		q = url.Values{"job_id": {jobID}}
	}
	data, err := c.do(ctx, http.MethodPost, path, q, args, http.StatusCreated)
	return strings.TrimSpace(string(data)), err
}

// Cancel cancels a queued or running job (OR-10).
func (c *Client) Cancel(ctx context.Context, workspace, id, reason string) error {
	_, err := c.do(ctx, http.MethodPost, ws(workspace)+"/jobs_u/queue/cancel/"+url.PathEscape(id), nil, map[string]string{"reason": reason}, http.StatusOK)
	return err
}

// Job is the part of GET /w/{ws}/jobs_u/get/{id} the walker reads: status,
// kind and topology (OR-11). Its args and result are not read, because the
// generic endpoint truncates large payloads (OR-V1).
type Job struct {
	Type        string      `json:"type"`
	ID          string      `json:"id"`
	ParentJob   *string     `json:"parent_job"`
	JobKind     string      `json:"job_kind"`
	Success     *bool       `json:"success"`
	Canceled    bool        `json:"canceled"`
	IsSkipped   bool        `json:"is_skipped"`
	CreatedAt   string      `json:"created_at"`
	StartedAt   string      `json:"started_at"`
	CompletedAt string      `json:"completed_at"`
	DurationMS  *int64      `json:"duration_ms"`
	ScriptPath  string      `json:"script_path"`
	FlowStatus  *FlowStatus `json:"flow_status"`
}

// FlowStatus is a flow job's flow_status (OR-11).
type FlowStatus struct {
	Step               int                `json:"step"`
	Modules            []FlowStatusModule `json:"modules"`
	FailureModule      *FlowStatusModule  `json:"failure_module"`
	PreprocessorModule *FlowStatusModule  `json:"preprocessor_module"`
	Retry              *FlowStatusRetry   `json:"retry"`
	UserStates         json.RawMessage    `json:"user_states"`
}

// FlowStatusRetry is a flow's retry record.
type FlowStatusRetry struct {
	FailCount  int      `json:"fail_count"`
	FailedJobs []string `json:"failed_jobs"`
}

// FlowStatusModule is one module entry of a flow_status (OR-11).
type FlowStatusModule struct {
	Type          string        `json:"type"`
	ID            string        `json:"id"`
	Job           *string       `json:"job"`
	FlowJobs      []string      `json:"flow_jobs"`
	HasFlowJobs   bool          `json:"-"`
	BranchChosen  *BranchChosen `json:"branch_chosen"`
	BranchAll     *struct{}     `json:"branchall"`
	Iterator      *struct{}     `json:"iterator"`
	FailedRetries []string      `json:"failed_retries"`
	Skipped       bool          `json:"skipped"`
	ParentModule  *string       `json:"parent_module"`
}

// UnmarshalJSON records whether flow_jobs was present, since an empty
// list (an empty loop) differs from no list (OR-26).
func (m *FlowStatusModule) UnmarshalJSON(data []byte) error {
	type plain FlowStatusModule
	var v plain
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	var probe struct {
		FlowJobs json.RawMessage `json:"flow_jobs"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	*m = FlowStatusModule(v)
	m.HasFlowJobs = probe.FlowJobs != nil && string(probe.FlowJobs) != "null"
	return nil
}

// BranchChosen is a branchone module's chosen branch.
type BranchChosen struct {
	Type   string `json:"type"`
	Branch *int   `json:"branch"`
}

// GetJob returns GET /w/{ws}/jobs_u/get/{id}?no_logs=true&no_code=true.
func (c *Client) GetJob(ctx context.Context, workspace, id string) (Job, error) {
	var j Job
	err := c.getJSON(ctx, ws(workspace)+"/jobs_u/get/"+url.PathEscape(id), url.Values{"no_logs": {"true"}, "no_code": {"true"}}, &j)
	return j, err
}

// GetArgs returns a job's full stored arguments (OR-V1).
func (c *Client) GetArgs(ctx context.Context, workspace, id string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, ws(workspace)+"/jobs_u/get_args/"+url.PathEscape(id), nil, nil, http.StatusOK)
}

// CreateVariable creates a variable (OR-31).
func (c *Client) CreateVariable(ctx context.Context, workspace, path, value string, secret bool, description string) error {
	body := map[string]any{"path": path, "value": value, "is_secret": secret, "description": description}
	_, err := c.do(ctx, http.MethodPost, ws(workspace)+"/variables/create", nil, body, http.StatusOK, http.StatusCreated)
	return err
}

// UpdateVariable sets an existing variable's value (oracle.setup each_run).
func (c *Client) UpdateVariable(ctx context.Context, workspace, path, value string) error {
	_, err := c.do(ctx, http.MethodPost, ws(workspace)+"/variables/update/"+itemPath(path), nil, map[string]any{"value": value}, http.StatusOK)
	return err
}

// VariableExists reports whether GET /w/{ws}/variables/get/{path} answers
// 200 (true) or 404 (false); any other status is an error (OR-31/b).
func (c *Client) VariableExists(ctx context.Context, workspace, path string) (bool, error) {
	_, err := c.do(ctx, http.MethodGet, ws(workspace)+"/variables/get/"+itemPath(path), nil, nil, http.StatusOK)
	var api *APIError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &api) && api.Status == http.StatusNotFound:
		return false, nil
	}
	return false, err
}

// CreateResource creates a resource (OR-31).
func (c *Client) CreateResource(ctx context.Context, workspace, path, resourceType string, value json.RawMessage, description string) error {
	body := map[string]any{"path": path, "resource_type": resourceType, "value": value, "description": description}
	_, err := c.do(ctx, http.MethodPost, ws(workspace)+"/resources/create", nil, body, http.StatusOK, http.StatusCreated)
	return err
}

// CreateResourceType creates a resource type (OR-31).
func (c *Client) CreateResourceType(ctx context.Context, workspace, name string, schema json.RawMessage, description string) error {
	body := map[string]any{"name": name, "description": description}
	if schema != nil {
		body["schema"] = schema
	}
	_, err := c.do(ctx, http.MethodPost, ws(workspace)+"/resources/type/create", nil, body, http.StatusOK, http.StatusCreated)
	return err
}
