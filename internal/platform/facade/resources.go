package facade

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/invakid404/windmill-lite/internal/platform"
)

// The resource writes that keep a step's state ([WA-13]): createResource,
// updateResourceValue and deleteResource for resources of type state,
// which the stock client's setState makes, answered from the services'
// ResourceStore (platform.Services.CreateStateResource and its siblings)
// with Windmill's statuses, content types and texts. The other writes keep
// a 501: a create of any other type or with an option the stock client
// never sends, and an update or delete of a stored entry of another type
// answer `Not implemented in windmill-lite: <route>`; an update or delete
// of a resource that the host or the bundle provides answers the
// restriction's 501 of ADR 0001, section 5a. Services without a store
// answer every write 501, as before.
//
// [WA-13]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-13
func (f *Facade) serveResourceWrites() {
	f.Handle("createResource", f.resourceCreate)
	f.Handle("updateResourceValue", f.resourceUpdate)
	f.Handle("deleteResource", f.resourceDelete)
}

// createOptions are the members of CreateResource that the stock client
// never sends, whose create keeps its 501.
var createOptions = []string{"description", "labels", "ws_specific", "preserve_on_behalf_of"}

// resourceCreate answers POST /w/{ws}/resources/create: the framework's
// rejections first (jsonBody), then CreateResource's missing members as
// the framework's 422 names them, then the create of a state.
func (f *Facade) resourceCreate(ctx context.Context, c *Call) Reply {
	s := f.providers(c)
	if s.ResourceStore == nil {
		return unsupported(c)
	}
	if refused := checkedJSONBody(c, platform.WriteBodySyntaxError); refused != nil {
		return *refused
	}
	fields, ok := objectFields(c.Body)
	if !ok {
		return plainText(422, "Failed to deserialize the JSON body into the target type: windmill-lite: the body is not a CreateResource object")
	}
	for _, name := range []string{"path", "resource_type"} {
		if _, ok := fields[name]; !ok {
			return missingField(c.Body, name)
		}
	}
	var path, typ string
	if !stringMember(fields["path"], &path) || !stringMember(fields["resource_type"], &typ) {
		return plainText(422, "Failed to deserialize the JSON body into the target type: windmill-lite: path and resource_type must be strings")
	}
	if typ != platform.StateType || c.Query.Has("update_if_exists") {
		return unsupported(c)
	}
	for _, name := range createOptions {
		if _, ok := fields[name]; ok {
			return unsupported(c)
		}
	}
	if err := s.CreateStateResource(ctx, path, fields["value"]); err != nil {
		return answer(nil, err)
	}
	return plainText(201, "resource "+path+" created")
}

// resourceUpdate answers POST /w/{ws}/resources/update_value/{path}: its
// body is an object whose value, which may be missing, becomes the stored
// state's.
func (f *Facade) resourceUpdate(ctx context.Context, c *Call) Reply {
	s := f.providers(c)
	if s.ResourceStore == nil {
		return unsupported(c)
	}
	if refused := checkedJSONBody(c, platform.WriteBodySyntaxError); refused != nil {
		return *refused
	}
	fields, ok := objectFields(c.Body)
	if !ok {
		return plainText(422, "Failed to deserialize the JSON body into the target type: windmill-lite: the body is not an object")
	}
	path := c.Params["path"]
	if err := s.UpdateStateResource(ctx, path, fields["value"]); err != nil {
		return writeRefused(c, err)
	}
	return plainText(200, "value of resource "+path+" updated")
}

// resourceDelete answers DELETE /w/{ws}/resources/delete/{path}.
func (f *Facade) resourceDelete(ctx context.Context, c *Call) Reply {
	s := f.providers(c)
	if s.ResourceStore == nil {
		return unsupported(c)
	}
	path := c.Params["path"]
	if err := s.DeleteStateResource(ctx, path); err != nil {
		return writeRefused(c, err)
	}
	return plainText(200, "resource "+path+" deleted")
}

// writeRefused is the answer to an update or a delete that the services
// refused: the restriction's 501 for a provided resource, the 501 of the
// writes windmill-lite does not make for an entry of another type, else
// the API's error.
func writeRefused(c *Call, err error) Reply {
	switch {
	case errors.Is(err, platform.ErrProvidedResource):
		return text(501, UnsupportedPrefix+c.Route.Name()+": "+err.Error())
	case errors.Is(err, platform.ErrNotStateResource):
		return unsupported(c)
	}
	return answer(nil, err)
}

// unsupported is the 501 of a call windmill-lite does not serve.
func unsupported(c *Call) Reply {
	return text(501, UnsupportedPrefix+c.Route.Name())
}

// objectFields reads a body that parses (jsonBody checked it) as a JSON
// object, by member name, as written; false when it is not an object. Of a
// member written twice, the last counts.
func objectFields(body []byte) (map[string]json.RawMessage, bool) {
	if !bytes.HasPrefix(bytes.TrimLeft(body, " \t\r\n"), []byte("{")) {
		return nil, false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return nil, false
	}
	return fields, true
}

// missingField is the framework's 422 for a body object that lacks a
// required member: "Failed to deserialize the JSON body into the target
// type: missing field `<name>` at line <l> column <c>", where the reader
// stood after the object's closing brace, the column counting the bytes of
// its line up to there ([WA-13]/f). Only compact bodies are recorded; for
// any other layout, the line and column are computed by the same rule.
//
// [WA-13]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/conformance.md#wa-13
func missingField(body []byte, name string) Reply {
	at := len(bytes.TrimRight(body, " \t\r\n"))
	line, start := 1, 0
	for i := 0; i < at; i++ {
		if body[i] == '\n' {
			line, start = line+1, i+1
		}
	}
	return plainText(422, fmt.Sprintf("Failed to deserialize the JSON body into the target type: missing field `%s` at line %d column %d", name, line, at-start))
}

// stringMember reads raw, a member's JSON text, into out when it is a JSON
// string. Null is not one: Go would read it as the empty string, where the
// backend's String field refuses it.
func stringMember(raw json.RawMessage, out *string) bool {
	return len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, out) == nil
}
