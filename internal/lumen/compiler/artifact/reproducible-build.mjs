import childProcess from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const image = "node:22.23.1-bookworm-slim@sha256:8607a9064d4a571140998ae9e52a3b3fcf9cff361d04642d5971e6cd76d39e27";
const upstreamCommit = "faff38c7a04e41815d6e63051ff126137d5820b9";
const upstreamTree = "9ec613ec5fa92672b53c652457c832cde3ffb175";
const [upstreamArgument] = process.argv.slice(2);
if (!upstreamArgument || process.argv.length !== 3) throw new Error("usage: reproducible-build.mjs <upstream>");
const upstream = path.resolve(upstreamArgument);
const source = path.dirname(fileURLToPath(import.meta.url));
const root = fs.mkdtempSync(path.join(os.tmpdir(), "gc-lumen-repro-"));
const git = (...args) => childProcess.execFileSync("git", ["-C", upstream, ...args], { encoding: "utf8" }).trim();
const assertPinnedUpstream = () => {
  if (git("rev-parse", "HEAD") !== upstreamCommit) throw new Error("upstream commit drift");
  if (git("rev-parse", "HEAD^{tree}") !== upstreamTree) throw new Error("upstream tree drift");
  if (git("status", "--porcelain", "--untracked-files=all") !== "") throw new Error("upstream checkout is not clean");
};
try {
	assertPinnedUpstream();
  const build = (name) => {
    const output = path.join(root, name);
    fs.mkdirSync(output);
    childProcess.execFileSync("docker", [
      "run", "--rm", "--platform", "linux/amd64",
      "-v", `${upstream}:/upstream:ro`, "-v", `${source}:/artifact:ro`, "-v", `${output}:/out`,
      image, "sh", "-ceu",
      "cp -a /artifact /tmp/artifact && node /tmp/artifact/build.mjs /upstream /out",
    ], { stdio: "inherit" });
    return output;
  };
  const first = build("first");
  const second = build("second");
	assertPinnedUpstream();
  for (const file of ["compiler.js", "manifest.json"]) {
    if (!fs.readFileSync(path.join(first, file)).equals(fs.readFileSync(path.join(second, file)))) throw new Error(`${file} is not reproducible`);
  }
  const manifest = JSON.parse(fs.readFileSync(path.join(first, "manifest.json"), "utf8"));
  const repeated = JSON.parse(fs.readFileSync(path.join(second, "manifest.json"), "utf8"));
  if (JSON.stringify(manifest.outputInputs) !== JSON.stringify(repeated.outputInputs) || JSON.stringify(manifest.outputImports) !== JSON.stringify(repeated.outputImports) || manifest.outputTreeSHA256 !== repeated.outputTreeSHA256) throw new Error("metafile or output tree is not reproducible");
  process.stdout.write(`${manifest.artifactSHA256}\n`);
} finally {
  fs.rmSync(root, { recursive: true, force: true });
}
