// Would print and fail at load; its metadata records a signature that did
// not parse, so the harness never loads it (WR-7, WR-37).
console.log("top-level code ran");
throw new Error("the module was loaded");
