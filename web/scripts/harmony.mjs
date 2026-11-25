import { cp, mkdir } from "node:fs/promises";
const target = new URL(
  "../harmony/entry/src/main/resources/rawfile/web/",
  import.meta.url,
);
await mkdir(target, { recursive: true });
await cp(new URL("../dist/", import.meta.url), target, { recursive: true });
