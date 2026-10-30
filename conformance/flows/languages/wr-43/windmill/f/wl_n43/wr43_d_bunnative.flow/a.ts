//native
export async function main() { Promise.reject(new Error("late")); await new Promise(r => setTimeout(r, 100)); return 1; }
