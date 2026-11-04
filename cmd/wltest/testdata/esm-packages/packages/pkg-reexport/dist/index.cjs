// The entry of the require condition: an import never reaches it.
exports.JobService = { getJob: (id) => ({ id, base: "the require entry" }) };
exports.settings = { base: "the require entry" };
