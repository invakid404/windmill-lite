# internal/jsonlib (study only)

The JSON-library study of branch `study/json-libraries`: encoding/json as
windmill-lite uses it today, against encoding/json/v2 and jsontext (Go
1.27), github.com/go-json-experiment/json and github.com/bytedance/sonic.
This directory, its dependencies in `go.mod` and the workflow
`.github/workflows/json-study.yml` exist only on that branch and are never
merged.

- `std`, `v2`, `gje`, `sonicjson`: one backend per library, each with the
  operations windmill-lite's call sites perform (validate, compact,
  unmarshal, marshal, an object's raw members by key or in order, decode,
  sort and encode).
- `bench`: the microbenchmarks (`BenchmarkLib`), the peak-RSS probe
  (`TestPeakRSS`), the exactness probes (`TestExactness`) and the payload
  families they share.

Go 1.27 runs the encoding/json v1 API on the v2 implementation by default;
`GOEXPERIMENT=nojsonv2` restores the classic implementation. The benchmark
binary names the v1 candidate after the engine it was built with: `v1`
(today's default) or `v1classic`.
