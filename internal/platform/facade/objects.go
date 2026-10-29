package facade

import (
	"context"
	"encoding/json"

	"github.com/invakid404/windmill-lite/internal/platform"
)

// The object store routes of ADR 0007's ObjectStore (milestone PF3): the
// job helpers loadS3File and writeS3File call (WA-16), for explicit keys.
// What the client contract leaves to the server (a generated key, a
// resource's own bucket) and deletion are `later` and answer 501, as does
// every call when no object store is configured.
func (f *Facade) serveObjects() {
	f.Handle("fileUpload", f.upload)
	f.Handle("fileDownload", f.download)
}

// objectRequest reads the object a job-helper call names: its storage
// (platform.DefaultStorage when it names none) and its key, or the 501 of
// what the object store does not serve.
func (f *Facade) objectRequest(c *Call) (storage, key string, refused *Reply) {
	refuse := func(why string) (string, string, *Reply) {
		r := text(501, UnsupportedPrefix+c.Route.Name()+" "+why)
		return "", "", &r
	}
	key = c.Query.Get("file_key")
	switch {
	case f.platform.Objects == nil:
		return refuse("(no object store is configured)")
	case key == "":
		return refuse("without file_key (a key the server generates)")
	case c.Query.Get("s3_resource_path") != "":
		return refuse("with s3_resource_path (a resource's own bucket)")
	}
	storage = c.Query.Get("storage")
	if storage == "" {
		storage = platform.DefaultStorage
	}
	return storage, key, nil
}

// upload answers POST /w/{ws}/job_helpers/upload_s3_file (WA-16): the raw
// body is stored at (storage, file_key) with the query's content_type and
// content_disposition, and the answer is {"file_key": <file_key>}, which
// writeS3File returns as {s3: …}.
func (f *Facade) upload(ctx context.Context, c *Call) Reply {
	storage, key, refused := f.objectRequest(c)
	if refused != nil {
		return *refused
	}
	o := platform.Object{Content: c.Body, ContentType: c.Query.Get("content_type"), ContentDisposition: c.Query.Get("content_disposition")}
	if err := f.platform.Objects.Put(ctx, storage, key, o); err != nil {
		return answer(nil, err)
	}
	body, err := json.Marshal(map[string]string{"file_key": key})
	return answer(body, err)
}

// download answers GET /w/{ws}/job_helpers/download_s3_file (WA-16): the
// object's content, with the content type its upload gave
// (application/octet-stream when none); an object that is not there
// answers 404, whose text is windmill-lite's (what a CE server answers is
// Q-WA-3).
func (f *Facade) download(ctx context.Context, c *Call) Reply {
	storage, key, refused := f.objectRequest(c)
	if refused != nil {
		return *refused
	}
	o, ok, err := f.platform.Objects.Get(ctx, storage, key)
	switch {
	case err != nil:
		return answer(nil, err)
	case !ok:
		return Error{Variant: "NotFound", Message: "windmill-lite: no object " + string(jsonString(key)) + " in storage " + string(jsonString(storage))}.Reply()
	}
	headers := map[string]string{"content-type": "application/octet-stream"}
	if o.ContentType != "" {
		headers["content-type"] = o.ContentType
	}
	if o.ContentDisposition != "" {
		headers["content-disposition"] = o.ContentDisposition
	}
	return Reply{Status: 200, Headers: headers, Body: o.Content}
}
