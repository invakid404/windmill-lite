class MyErr extends Error {
  code = 42;
  constructor(message: string) {
    super(message);
    this.name = "MyErr";
  }
}
export async function main() {
  throw new MyErr("boom");
}
