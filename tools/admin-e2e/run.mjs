import {
  chmodSync,
  copyFileSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  openSync,
  readFileSync,
  rmSync,
  statSync,
  writeFileSync,
  closeSync,
} from "node:fs";
import { spawn, spawnSync } from "node:child_process";
import { tmpdir } from "node:os";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { createAdminDatabaseFixture } from "./database-fixture.mjs";

const projectRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const adminRoot = resolve(projectRoot, "admin");
const maintenanceEnvPath = process.env.TBCG_ADMIN_E2E_MAINTENANCE_ENV_FILE;
const caTool = "/opt/homebrew/bin/certutil";
const openssl = "/usr/bin/openssl";
const tempRoot = mkdtempSync(resolve(tmpdir(), "tbcg-admin-e2e-"));
chmodSync(tempRoot, 0o700);
let child;
let databaseFixture;
let interrupted = false;

function run(command, args, options = {}) {
  const result = spawnSync(command, args, { encoding: "utf8", ...options });
  if (result.error || result.status !== 0) {
    throw new Error(
      `${command} failed (${result.status ?? result.error?.message ?? "unknown"})`,
    );
  }
  return result.stdout;
}

function isSensitiveEnvironmentKey(key) {
  return (
    key.startsWith("TBCG_") ||
    key.startsWith("PG") ||
    key === "NODE_EXTRA_CA_CERTS" ||
    /(DATABASE_URL|PASSWORD|PASSWD|TOKEN|SECRET|PRIVATE_KEY|API_KEY|DSN)/i.test(
      key,
    )
  );
}

function readPrivateEnvironment(path) {
  if (!path) {
    throw new Error(
      "A private 0600 generated business environment JSON file is required.",
    );
  }
  const mode = statSync(path).mode & 0o777;
  if ((mode & 0o077) !== 0) {
    throw new Error(
      "The E2E environment JSON must not be accessible by group or other users.",
    );
  }
  const parsed = JSON.parse(readFileSync(path, "utf8"));
  if (parsed === null || Array.isArray(parsed) || typeof parsed !== "object") {
    throw new Error(
      "The E2E environment JSON must contain an object of string values.",
    );
  }
  const entries = Object.entries(parsed);
  if (
    entries.some(
      ([key, value]) =>
        !/^(TBCG_ADMIN_DATABASE_URL|TBCG_ADMIN_E2E_[A-Z0-9_]+)$/.test(key) ||
        typeof value !== "string",
    )
  ) {
    throw new Error(
      "E2E environment keys must use TBCG_ADMIN_E2E_* and all values must be strings.",
    );
  }
  for (const role of ["READER", "AUTHOR", "REVIEWER", "PUBLISHER"]) {
    for (const suffix of ["USERNAME", "PASSWORD"]) {
      if (typeof parsed[`TBCG_ADMIN_E2E_${role}_${suffix}`] !== "string") {
        throw new Error(
          `Missing TBCG_ADMIN_E2E_${role}_${suffix} in the private environment JSON.`,
        );
      }
    }
  }
  const allowedKeys = new Set([
    "TBCG_ADMIN_DATABASE_URL",
    "TBCG_ADMIN_E2E_BASE_URL",
    ...["READER", "AUTHOR", "REVIEWER", "PUBLISHER"].flatMap((role) => [
      `TBCG_ADMIN_E2E_${role}_USERNAME`,
      `TBCG_ADMIN_E2E_${role}_PASSWORD`,
    ]),
  ]);
  if (entries.some(([key]) => !allowedKeys.has(key))) {
    throw new Error(
      "The private environment JSON contains unsupported E2E keys.",
    );
  }
  if (
    typeof parsed.TBCG_ADMIN_DATABASE_URL !== "string" ||
    parsed.TBCG_ADMIN_DATABASE_URL.length === 0
  ) {
    throw new Error(
      "Missing TBCG_ADMIN_DATABASE_URL in the private environment JSON.",
    );
  }
  return Object.fromEntries(entries);
}

function createCertificateAuthority() {
  const certDir = resolve(tempRoot, "certs");
  mkdirSync(certDir, { mode: 0o700 });
  const caKey = resolve(certDir, "ca.key");
  const caCert = resolve(certDir, "ca.crt");
  const leafKey = resolve(certDir, "localhost.key");
  const leafCsr = resolve(certDir, "localhost.csr");
  const leafCert = resolve(certDir, "localhost.crt");
  const extensions = resolve(certDir, "localhost.ext");
  writeFileSync(
    extensions,
    "subjectAltName=DNS:localhost,IP:127.0.0.1\nextendedKeyUsage=serverAuth\nbasicConstraints=CA:FALSE\n",
    { mode: 0o600 },
  );
  run(openssl, [
    "req",
    "-x509",
    "-newkey",
    "rsa:2048",
    "-nodes",
    "-sha256",
    "-days",
    "2",
    "-subj",
    "/CN=TBCG E2E Private CA",
    "-keyout",
    caKey,
    "-out",
    caCert,
    "-addext",
    "basicConstraints=critical,CA:TRUE",
    "-addext",
    "keyUsage=critical,keyCertSign,cRLSign",
  ]);
  run(openssl, [
    "req",
    "-new",
    "-newkey",
    "rsa:2048",
    "-nodes",
    "-sha256",
    "-subj",
    "/CN=localhost",
    "-keyout",
    leafKey,
    "-out",
    leafCsr,
  ]);
  run(openssl, [
    "x509",
    "-req",
    "-in",
    leafCsr,
    "-CA",
    caCert,
    "-CAkey",
    caKey,
    "-CAcreateserial",
    "-out",
    leafCert,
    "-days",
    "2",
    "-sha256",
    "-extfile",
    extensions,
  ]);
  return { certDir, caCert, leafCert, leafKey };
}

function createNssProfile(name, caCert, includeCA) {
  const profile = resolve(tempRoot, "profiles", name);
  mkdirSync(profile, { recursive: true, mode: 0o700 });
  run(caTool, ["-N", "--empty-password", "-d", `sql:${profile}`]);
  if (includeCA) {
    run(caTool, [
      "-A",
      "-d",
      `sql:${profile}`,
      "-n",
      "TBCG disposable E2E CA",
      "-t",
      "C,,",
      "-a",
      "-i",
      caCert,
    ]);
  }
  return profile;
}

function stopChild(signal = "SIGTERM") {
  if (!child || child.exitCode !== null || child.signalCode !== null) return;
  try {
    process.kill(-child.pid, signal);
  } catch {
    child.kill(signal);
  }
}

async function main() {
  databaseFixture = createAdminDatabaseFixture(maintenanceEnvPath, tempRoot);
  const privateEnvironment = readPrivateEnvironment(
    databaseFixture.businessEnvironmentPath,
  );
  const baseURL =
    privateEnvironment.TBCG_ADMIN_E2E_BASE_URL ?? "https://127.0.0.1:8443";
  const runID = new Date().toISOString().replaceAll(":", "-");
  const logDirectory = resolve(projectRoot, "admin/test-results/secure-e2e");
  const screenshotPath = resolve(logDirectory, `safe-login-${runID}.png`);
  const base = new URL(baseURL);
  if (
    base.protocol !== "https:" ||
    base.username !== "" ||
    base.password !== "" ||
    !["localhost", "127.0.0.1"].includes(base.hostname) ||
    base.port === "" ||
    Number(base.port) < 1024 ||
    base.pathname !== "/" ||
    base.search !== "" ||
    base.hash !== ""
  ) {
    throw new Error(
      "Base URL must be a local HTTPS origin on localhost or 127.0.0.1.",
    );
  }
  const bindHost = base.hostname === "localhost" ? "127.0.0.1" : base.hostname;
  const { caCert, leafCert, leafKey } = createCertificateAuthority();
  const profilesRoot = resolve(tempRoot, "profiles");
  mkdirSync(profilesRoot, { mode: 0o700 });
  const profileMap = {
    AUTHOR_A: createNssProfile("author-a", caCert, true),
    AUTHOR_B: createNssProfile("author-b", caCert, true),
    REVIEWER: createNssProfile("reviewer", caCert, true),
    READER: createNssProfile("reader", caCert, true),
    PUBLISHER: createNssProfile("publisher", caCert, true),
    UNTRUSTED: createNssProfile("untrusted", caCert, false),
  };
  const serverCert = resolve(tempRoot, "server.crt");
  const serverKey = resolve(tempRoot, "server.key");
  copyFileSync(leafCert, serverCert);
  copyFileSync(leafKey, serverKey);
  chmodSync(serverKey, 0o600);
  const profileEnvironment = Object.fromEntries(
    Object.entries(profileMap).map(([name, path]) => [
      `TBCG_ADMIN_E2E_PROFILE_${name}`,
      path,
    ]),
  );
  const {
    TBCG_ADMIN_DATABASE_URL: serverDatabaseURL,
    ...playwrightEnvironment
  } = privateEnvironment;
  const serverEnvironmentPath = resolve(tempRoot, "admin-server-env.json");
  const inheritedEnvironment = Object.fromEntries(
    Object.entries(process.env).filter(
      ([key]) => !isSensitiveEnvironmentKey(key),
    ),
  );
  const environment = {
    ...inheritedEnvironment,
    ...playwrightEnvironment,
    ...profileEnvironment,
    TBCG_ADMIN_E2E_SERVER_ENV_FILE: serverEnvironmentPath,
    NODE_EXTRA_CA_CERTS: caCert,
    GOTOOLCHAIN: "go1.26.4",
    TBCG_ADMIN_TLS_CERT: serverCert,
    TBCG_ADMIN_TLS_KEY: serverKey,
    TBCG_ADMIN_BIND: `${bindHost}:${base.port}`,
    TBCG_ADMIN_ORIGIN: base.origin,
    TBCG_PROJECT_ROOT: projectRoot,
    TBCG_ADMIN_E2E_BASE_URL: base.origin,
    TBCG_ADMIN_E2E_OUTPUT_DIR: resolve(tempRoot, "playwright-output"),
    TBCG_ADMIN_E2E_SAFE_SCREENSHOT_PATH: screenshotPath,
    PLAYWRIGHT_SKIP_BROWSER_GC: "1",
  };
  const buildEnvironment = Object.fromEntries(
    Object.entries(process.env).filter(
      ([key]) => !isSensitiveEnvironmentKey(key),
    ),
  );
  run("pnpm", ["run", "build"], {
    cwd: adminRoot,
    env: buildEnvironment,
    stdio: "ignore",
  });
  writeFileSync(
    serverEnvironmentPath,
    JSON.stringify({ TBCG_ADMIN_DATABASE_URL: serverDatabaseURL }),
    { mode: 0o600 },
  );
  mkdirSync(logDirectory, { recursive: true, mode: 0o700 });
  if ((statSync(logDirectory).mode & 0o077) !== 0) {
    throw new Error(
      "The E2E log directory must not be accessible by group or other users.",
    );
  }
  const logPath = resolve(logDirectory, `run-${runID}.log`);
  const logFD = openSync(logPath, "wx", 0o600);
  const output = [];
  child = spawn(
    "pnpm",
    ["exec", "playwright", "test", "--config", "playwright.config.ts"],
    {
      cwd: adminRoot,
      env: environment,
      detached: true,
      stdio: ["ignore", "pipe", "pipe"],
    },
  );
  child.stdout.on("data", (chunk) => output.push(chunk));
  child.stderr.on("data", (chunk) => output.push(chunk));
  const exitCode = await new Promise((resolveExit) => {
    child.once("error", () => resolveExit(1));
    child.once("close", (code, signal) =>
      resolveExit(code ?? (signal ? 1 : 0)),
    );
  });
  const redactedOutput = Object.values(privateEnvironment).reduce(
    (current, secret) =>
      secret.length > 0 ? current.replaceAll(secret, "[REDACTED]") : current,
    Buffer.concat(output).toString(),
  );
  writeFileSync(logFD, redactedOutput);
  closeSync(logFD);
  process.stdout.write(redactedOutput);
  if (interrupted) return 130;
  process.stdout.write(
    `E2E exit code: ${exitCode}; private run log: ${logPath}\n`,
  );
  process.stdout.write(
    existsSync(screenshotPath)
      ? `No-credential screenshot: ${screenshotPath}\n`
      : "No safe screenshot was captured before failure.\n",
  );
  return exitCode;
}

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => {
    interrupted = true;
    stopChild(signal);
  });
}

try {
  const code = await main();
  process.exitCode = code;
} catch (error) {
  const message =
    error instanceof Error ? error.message : "Unexpected E2E harness error";
  process.stderr.write(`E2E harness failed: ${message}\n`);
  process.exitCode = 1;
} finally {
  stopChild();
  if (databaseFixture !== undefined) {
    try {
      databaseFixture.cleanup();
      process.stdout.write("Owned E2E appDB and role cleanup completed.\n");
    } catch (error) {
      const message =
        error instanceof Error
          ? error.message
          : "Owned E2E database cleanup failed.";
      process.stderr.write(`E2E cleanup failed: ${message}\n`);
      process.exitCode = 1;
    }
  }
  rmSync(tempRoot, { recursive: true, force: true });
}
