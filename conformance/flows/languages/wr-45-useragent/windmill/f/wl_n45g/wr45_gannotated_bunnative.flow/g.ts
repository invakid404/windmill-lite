//native
//useragent probe/1
export async function main(sink: string) { const r = await fetch(sink + "/user-agent"); return await r.text(); }
