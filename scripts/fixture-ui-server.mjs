import { build } from "esbuild";
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";

const port = Number(process.env.PLAYWRIGHT_PORT || 4173);
const outfile = ".fixture-ui/fixture.js";

await build({
  entryPoints: ["ui/src/fixture-browser.tsx"],
  bundle: true,
  format: "esm",
  platform: "browser",
  target: "es2022",
  outfile,
  sourcemap: true,
});

const fixture = await readFile(outfile);
const sourceMap = await readFile(`${outfile}.map`);
const html = Buffer.from(`<!doctype html><html><head><meta charset="utf-8"><title>Coordinator fixture</title></head><body><div id="root"></div><script type="module" src="/fixture.js"></script></body></html>`);

createServer((request, response) => {
  const path = new URL(request.url || "/", `http://127.0.0.1:${port}`).pathname;
  if (path === "/fixture.js") {
    response.writeHead(200, { "content-type": "text/javascript" });
    response.end(fixture);
  } else if (path === "/fixture.js.map") {
    response.writeHead(200, { "content-type": "application/json" });
    response.end(sourceMap);
  } else {
    response.writeHead(200, { "content-type": "text/html" });
    response.end(html);
  }
}).listen(port, "127.0.0.1", () => console.log(`fixture UI listening on ${port}`));
