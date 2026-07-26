// scratchpad extension for pi (https://github.com/badlogic/pi-mono).
// pi has no native MCP support, so this registers the same two tools the
// scratchpad MCP server exposes, writing directly to the artifact root.
// Install: copy to ~/.pi/agent/extensions/scratchpad.ts  (or `make register-pi`)
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
import { Type } from "typebox";
import * as fs from "node:fs";
import * as path from "node:path";
import * as os from "node:os";

const ROOT = process.env.SCRATCHPAD_ROOT ?? path.join(os.homedir(), ".scratchpad");
const BASE_URL = process.env.SCRATCHPAD_URL ?? "http://localhost:8737";
const NAME_RE = /^[a-zA-Z0-9][a-zA-Z0-9._-]{0,99}$/;

function checkSegs(p: string, what: string): string[] {
  const segs = p.split("/").filter(Boolean);
  for (const s of segs) {
    if (!NAME_RE.test(s)) throw new Error(`invalid ${what} segment ${JSON.stringify(s)}: must match ${NAME_RE}`);
  }
  return segs;
}

function hasHtml(dir: string): boolean {
  try {
    return fs
      .readdirSync(dir, { withFileTypes: true })
      .some((e) => e.isFile() && e.name.toLowerCase().endsWith(".html"));
  } catch {
    return false;
  }
}

function text(s: string) {
  return { content: [{ type: "text" as const, text: s }], details: {} };
}

export default function (pi: ExtensionAPI) {
  pi.registerTool({
    name: "publish_artifact",
    label: "Publish artifact",
    description:
      `Create a NEW artifact from a set of files, hosted immediately at ${BASE_URL}/a/<project>/<name>/. ` +
      "Create-only: fails if the name exists — deletion is a human action in the scratchpad web UI, so pick a fresh name instead (check list_artifacts). " +
      "At least one top-level .html is required (index.html preferred). Any other relative files are allowed (css, js, images, subfolders); " +
      "set base64:true for binary content. Reference assets with relative URLs from the html.",
    parameters: Type.Object({
      project: Type.Optional(Type.String({ description: "Optional project path to group under, e.g. demos or demos/charts" })),
      name: Type.String({ description: "New artifact name; must not already exist" }),
      files: Type.Array(
        Type.Object({
          path: Type.String({ description: "Relative path inside the artifact, e.g. index.html or img/logo.png" }),
          content: Type.String({ description: "File content; base64-encoded when base64 is true" }),
          base64: Type.Optional(Type.Boolean({ description: "Set true for binary files" })),
        }),
        { description: "The artifact's files" },
      ),
    }),
    async execute(_toolCallId, params) {
      const projSegs = params.project ? checkSegs(params.project, "project") : [];
      checkSegs(params.name, "name");
      if (params.name.includes("/")) throw new Error("name must be a single segment; use project for grouping");
      let entry = false;
      for (const f of params.files) {
        checkSegs(f.path, "file path");
        if (!f.path.includes("/") && f.path.toLowerCase().endsWith(".html")) entry = true;
      }
      if (!entry) throw new Error("at least one top-level .html file is required (index.html preferred)");

      const dir = path.join(ROOT, ...projSegs, params.name);
      if (fs.existsSync(dir)) {
        throw new Error(`"${[...projSegs, params.name].join("/")}" already exists — names are not reusable until a human deletes the old artifact in the web UI; pick a different name`);
      }
      for (let i = 1; i <= projSegs.length; i++) {
        if (hasHtml(path.join(ROOT, ...projSegs.slice(0, i)))) {
          throw new Error(`"${projSegs.slice(0, i).join("/")}" is an artifact, not a project — artifacts cannot contain artifacts`);
        }
      }
      fs.mkdirSync(dir, { recursive: true });
      for (const f of params.files) {
        const abs = path.join(dir, ...f.path.split("/"));
        fs.mkdirSync(path.dirname(abs), { recursive: true });
        fs.writeFileSync(abs, f.base64 ? Buffer.from(f.content, "base64") : f.content);
      }
      const rel = [...projSegs, params.name].join("/");
      return text(`published ${dir}\n${BASE_URL}/a/${rel}/`);
    },
  });

  pi.registerTool({
    name: "list_artifacts",
    label: "List artifacts",
    description: "List all artifacts currently hosted on the scratchpad.",
    parameters: Type.Object({}),
    async execute() {
      const rows: string[] = [];
      const walk = (dir: string, project: string) => {
        let entries: fs.Dirent[];
        try {
          entries = fs.readdirSync(dir, { withFileTypes: true });
        } catch {
          return;
        }
        for (const e of entries) {
          if (!e.isDirectory() || e.name.startsWith(".")) continue;
          const sub = path.join(dir, e.name);
          const rel = project ? `${project}/${e.name}` : e.name;
          if (hasHtml(sub)) {
            rows.push(`${rel}  ${BASE_URL}/a/${rel}/`); // artifact: don't descend into assets
          } else {
            walk(sub, rel);
          }
        }
      };
      walk(ROOT, "");
      return text(rows.length ? rows.join("\n") : "no artifacts");
    },
  });
}
