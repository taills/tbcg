import { readFileSync, statSync } from "node:fs";
import { spawn } from "node:child_process";

// Starts the real content-build-worker with only its restricted database URL from a private file.
const workerConfigPath = process.env.TBCG_BUILD_WORKER_ENV_FILE;
if (workerConfigPath === undefined) {
  throw new Error("Missing private build worker environment file path.");
}
if ((statSync(workerConfigPath).mode & 0o077) !== 0) {
  throw new Error(
    "The private build worker file is accessible to other users.",
  );
}
const workerConfig = JSON.parse(readFileSync(workerConfigPath, "utf8"));
if (
  workerConfig === null ||
  Array.isArray(workerConfig) ||
  typeof workerConfig !== "object" ||
  Object.keys(workerConfig).length !== 1 ||
  typeof workerConfig.TBCG_BUILD_WORKER_DATABASE_URL !== "string" ||
  workerConfig.TBCG_BUILD_WORKER_DATABASE_URL.length === 0
) {
  throw new Error(
    "The private build worker environment file has an invalid schema.",
  );
}

const inheritedKeys = [
  "PATH",
  "HOME",
  "TMPDIR",
  "TMP",
  "TEMP",
  "LANG",
  "LC_ALL",
  "LC_CTYPE",
  "USER",
  "LOGNAME",
  "SHELL",
  "GOTOOLCHAIN",
  "GOROOT",
  "GOMODCACHE",
  "GOCACHE",
  "GOPROXY",
  "GOSUMDB",
  "TBCG_BUILD_WORKER_ID",
  "TBCG_BUILD_ARTIFACT_DIR",
  "TBCG_BUILD_WORKER_POLL_INTERVAL",
  "TBCG_PROJECT_ROOT",
];
const workerEnvironment = {
  ...Object.fromEntries(
    inheritedKeys
      .filter((key) => process.env[key] !== undefined)
      .map((key) => [key, process.env[key]]),
  ),
  TBCG_BUILD_WORKER_DATABASE_URL: workerConfig.TBCG_BUILD_WORKER_DATABASE_URL,
};

const worker = spawn("go", ["run", "./cmd/content-build-worker"], {
  cwd: process.cwd(),
  env: workerEnvironment,
  stdio: "inherit",
});

function stopWorker(signal) {
  if (worker.exitCode !== null || worker.signalCode !== null) return;
  worker.kill(signal);
}

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => stopWorker(signal));
}

await new Promise((resolveExit) => {
  worker.once("error", () => {
    process.stderr.write("Unable to start the content build worker.\n");
    resolveExit(1);
  });
  worker.once("close", (code, signal) => resolveExit(code ?? (signal ? 1 : 0)));
}).then((code) => {
  process.exitCode = code;
});
