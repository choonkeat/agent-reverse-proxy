#!/usr/bin/env node

import { spawnSync, execFileSync } from "child_process";
import { createRequire } from "module";
import { existsSync, chmodSync } from "fs";
import { dirname, join } from "path";
import { fileURLToPath } from "url";

const require = createRequire(import.meta.url);
const __dirname = dirname(fileURLToPath(import.meta.url));

const PLATFORM_MAP = {
  linux: "linux",
  darwin: "darwin",
  win32: "win32",
};

const ARCH_MAP = {
  x64: "x64",
  arm64: "arm64",
};

const platform = PLATFORM_MAP[process.platform];
const arch = ARCH_MAP[process.arch];

if (!platform || !arch) {
  console.error(
    `Unsupported platform: ${process.platform}-${process.arch}\n` +
      `agent-reverse-proxy supports: linux-x64, linux-arm64, darwin-x64, darwin-arm64, win32-x64, win32-arm64`
  );
  process.exit(1);
}

const pkgName = `@choonkeat/agent-reverse-proxy-${platform}-${arch}`;
const binName = process.platform === "win32" ? "agent-reverse-proxy.exe" : "agent-reverse-proxy";

function resolveBinPath() {
  try {
    const pkgDir = dirname(require.resolve(`${pkgName}/package.json`));
    return join(pkgDir, "bin", binName);
  } catch {
    return null;
  }
}

let binPath = resolveBinPath();

// optionalDependencies may not be installed (e.g. npx) — install on demand
if (!binPath) {
  try {
    console.error(`Installing ${pkgName}...`);
    execFileSync("npm", ["install", "--no-save", pkgName], {
      stdio: "inherit",
      cwd: join(__dirname, ".."),
    });
    binPath = resolveBinPath();
  } catch {
    // ignore — fall through to local/error path
  }
}

// Fallback: check for local build in npm-platforms/ (development)
if (!binPath) {
  const localPath = join(__dirname, "..", "npm-platforms", `${platform}-${arch}`, "bin", binName);
  if (existsSync(localPath)) {
    binPath = localPath;
  }
}

if (!binPath) {
  console.error(
    `Could not find package ${pkgName}.\n` +
      `Make sure it is installed — this usually means your platform is supported\n` +
      `but the optional dependency was not installed.\n\n` +
      `Try: npm install ${pkgName}\n` +
      `Or run: npx @choonkeat/agent-reverse-proxy`
  );
  process.exit(1);
}

if (!existsSync(binPath)) {
  console.error(`Binary not found at ${binPath}`);
  process.exit(1);
}

function run() {
  const result = spawnSync(binPath, process.argv.slice(2), {
    stdio: "inherit",
  });

  if (result.error) {
    return result;
  }
  process.exit(result.status ?? 1);
}

let result = run();

// Handle EACCES by chmod +x and retrying
if (result.error && result.error.code === "EACCES") {
  try {
    chmodSync(binPath, 0o755);
  } catch (e) {
    console.error(`Failed to chmod +x ${binPath}: ${e.message}`);
    process.exit(1);
  }
  result = run();
}

if (result.error) {
  console.error(`Failed to start agent-reverse-proxy: ${result.error.message}`);
  process.exit(1);
}
