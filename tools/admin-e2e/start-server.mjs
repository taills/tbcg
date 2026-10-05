import { readFileSync, statSync } from "node:fs";
import { spawn } from "node:child_process";

const serverConfigPath = process.env.TBCG_ADMIN_E2E_SERVER_ENV_FILE;
if (serverConfigPath === undefined) {
  throw new Error("Missing private admin-server environment file path.");
}
if ((statSync(serverConfigPath).mode & 0o077) !== 0) {
  throw new Error(
    "The private admin-server file is accessible to other users.",
  );
}
const serverConfig = JSON.parse(readFileSync(serverConfigPath, "utf8"));
if (
  serverConfig === null ||
  Array.isArray(serverConfig) ||
  typeof serverConfig !== "object" ||
  Object.keys(serverConfig).length !== 1 ||
  typeof serverConfig.TBCG_ADMIN_DATABASE_URL !== "string" ||
  serverConfig.TBCG_ADMIN_DATABASE_URL.length === 0
) {
  throw new Error(
    "The private admin-server environment file has an invalid schema.",
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
  "TBCG_ADMIN_BIND",
  "TBCG_ADMIN_ORIGIN",
  "TBCG_ADMIN_TLS_CERT",
  "TBCG_ADMIN_TLS_KEY",
  "TBCG_PROJECT_ROOT",
];
const serverEnvironment = {
  ...Object.fromEntries(
    inheritedKeys
      .filter((key) => process.env[key] !== undefined)
      .map((key) => [key, process.env[key]]),
  ),
  TBCG_ADMIN_DATABASE_URL: serverConfig.TBCG_ADMIN_DATABASE_URL,
};

const server = spawn("go", ["run", "./cmd/admin-server"], {
  cwd: process.cwd(),
  env: serverEnvironment,
  stdio: "inherit",
});

function stopServer(signal) {
  if (server.exitCode !== null || server.signalCode !== null) return;
  server.kill(signal);
}

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => stopServer(signal));
}

await new Promise((resolveExit) => {
  server.once("error", () => {
    process.stderr.write("Unable to start the admin Go server.\n");
    resolveExit(1);
  });
  server.once("close", (code, signal) => resolveExit(code ?? (signal ? 1 : 0)));
}).then((code) => {
  process.exitCode = code;
});
