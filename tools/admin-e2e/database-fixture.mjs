import { randomBytes } from "node:crypto";
import { mkdirSync, readFileSync, statSync, writeFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const projectRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const serverRoot = resolve(projectRoot, "server");
const maintenanceConfirmation = "I_UNDERSTAND_TEST_DB_IS_DISPOSABLE";
const bootstrapConfirmation = "I_UNDERSTAND_LOCAL_ADMIN_BOOTSTRAP";

const ternPath = "/tmp/tbcg-tern-bin/tern";
const noLoginRole = "tbcg_admin_control_plane";
const pgURLParameters = new Map([
  ["sslmode", "PGSSLMODE"],
  ["sslrootcert", "PGSSLROOTCERT"],
  ["sslcert", "PGSSLCERT"],
  ["sslkey", "PGSSLKEY"],
  ["connect_timeout", "PGCONNECT_TIMEOUT"],
  ["application_name", "PGAPPNAME"],
  ["target_session_attrs", "PGTARGETSESSIONATTRS"],
  ["gssencmode", "PGGSSENCMODE"],
]);

function safeProcessEnvironment() {
  const allowed = [
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
  ];
  return Object.fromEntries(
    allowed
      .filter((key) => process.env[key] !== undefined)
      .map((key) => [key, process.env[key]]),
  );
}

function parseMaintenanceURL(value, caCertificatePath) {
  let parsed;
  try {
    parsed = new URL(value);
  } catch {
    throw new Error("TBCG_TEST_DATABASE_URL is not a valid PostgreSQL URL.");
  }
  if (!["postgres:", "postgresql:"].includes(parsed.protocol)) {
    throw new Error("Maintenance URL must use a PostgreSQL scheme.");
  }
  if (parsed.hostname !== "127.0.0.1" || parsed.port !== "55432") {
    throw new Error("Maintenance URL must use loopback host and port 55432.");
  }
  if (parsed.pathname !== "/postgres" || parsed.hash !== "") {
    throw new Error(
      "Maintenance URL must target the postgres maintenance database.",
    );
  }
  if (parsed.username.length === 0 || parsed.password.length === 0) {
    throw new Error("Maintenance URL must include credentials.");
  }
  for (const key of parsed.searchParams.keys()) {
    if (!pgURLParameters.has(key)) {
      throw new Error(`Unsupported maintenance URL option: ${key}`);
    }
  }
  if (typeof caCertificatePath !== "string" || caCertificatePath.length === 0) {
    throw new Error("A public PostgreSQL CA certificate path is required.");
  }
  parsed.searchParams.set("sslmode", "verify-full");
  parsed.searchParams.set("sslrootcert", resolve(caCertificatePath));
  parsed.searchParams.delete("sslcert");
  parsed.searchParams.delete("sslkey");
  return parsed;
}

export function parseMaintenanceEnvironment(
  values,
  inheritedEnvironment = {},
  caCertificatePath,
) {
  if (values === null || Array.isArray(values) || typeof values !== "object") {
    throw new Error("Maintenance environment JSON must contain an object.");
  }
  const allowedKeys = new Set([
    "TBCG_TEST_DATABASE_URL",
    "TBCG_TEST_DATABASE_CONFIRM",
    "TBCG_REQUIRE_TEST_DATABASE",
    "GOTOOLCHAIN",
    "root",
  ]);
  if (Object.keys(values).some((key) => !allowedKeys.has(key))) {
    throw new Error("Maintenance environment JSON contains unsupported keys.");
  }
  if (
    values.TBCG_TEST_DATABASE_CONFIRM !== maintenanceConfirmation ||
    values.TBCG_REQUIRE_TEST_DATABASE !== "1"
  ) {
    throw new Error("Missing disposable database confirmation or Require=1.");
  }
  if (values.GOTOOLCHAIN !== "go1.26.4") {
    throw new Error("Maintenance environment must select Go 1.26.4.");
  }
  if (typeof values.root !== "string" || values.root.length === 0) {
    throw new Error("Maintenance root metadata is missing.");
  }
  for (const key of ["TBCG_TEST_DATABASE_URL"]) {
    if (typeof values[key] !== "string" || values[key].length === 0) {
      throw new Error(`Missing ${key}.`);
    }
  }
  for (const key of ["PGHOSTADDR", "PGSERVICE", "PGSERVICEFILE"]) {
    if (
      typeof inheritedEnvironment[key] === "string" &&
      inheritedEnvironment[key] !== ""
    ) {
      throw new Error(`Refusing inherited ${key} override.`);
    }
  }
  const url = parseMaintenanceURL(
    values.TBCG_TEST_DATABASE_URL,
    caCertificatePath,
  );
  return {
    url,
    host: url.hostname,
    port: Number(url.port),
    database: "postgres",
    user: decodeURIComponent(url.username),
    password: decodeURIComponent(url.password),
    parameters: new Map(url.searchParams),
    toolchain: "go1.26.4",
  };
}

function readMaintenanceEnvironment(path) {
  const mode = statSync(path).mode & 0o777;
  if ((mode & 0o077) !== 0) {
    throw new Error(
      "Maintenance environment JSON must be private (0600 or stricter).",
    );
  }
  let parsed;
  try {
    parsed = JSON.parse(readFileSync(path, "utf8"));
  } catch {
    throw new Error(
      "Unable to parse private PostgreSQL maintenance environment JSON.",
    );
  }
  const rootCertificatePath = process.env.TBCG_ADMIN_E2E_PG_CA_CERT;
  try {
    if (
      rootCertificatePath === undefined ||
      !statSync(rootCertificatePath).isFile()
    ) {
      throw new Error();
    }
  } catch {
    throw new Error("Maintenance TLS CA file is unavailable.");
  }
  return parseMaintenanceEnvironment(parsed, process.env, rootCertificatePath);
}

function randomIdentifier(prefix) {
  return `${prefix}${randomBytes(12).toString("hex")}`;
}

function credentials(role) {
  return {
    [`TBCG_ADMIN_E2E_${role}_USERNAME`]: randomIdentifier(
      `e2e-${role.toLowerCase()}-`,
    ),
    [`TBCG_ADMIN_E2E_${role}_PASSWORD`]: randomBytes(32).toString("hex"),
  };
}

function maintenanceProcessEnvironment(config, database) {
  const parameterEnvironment = Object.fromEntries(
    [...pgURLParameters].flatMap(([parameter, key]) => {
      const value = config.parameters.get(parameter);
      return value === undefined || value === "" ? [] : [[key, value]];
    }),
  );
  return {
    ...safeProcessEnvironment(),
    ...parameterEnvironment,
    PGHOST: config.host,
    PGPORT: String(config.port),
    PGUSER: config.user,
    PGPASSWORD: config.password,
    PGDATABASE: database,
    PGCONNECT_TIMEOUT: parameterEnvironment.PGCONNECT_TIMEOUT ?? "5",
  };
}

function classifyBootstrapFailure(output) {
  const safeReasons = [
    [
      "cannot connect to the configured local bootstrap database",
      "DB_CONNECTION",
    ],
    [
      "cannot create bootstrap admin; username may already exist",
      "USERNAME_CONFLICT",
    ],
    [
      "cannot bind bootstrap role; check the Game ID and migration state",
      "ROLE_BINDING",
    ],
    ["cannot audit bootstrap account creation", "AUDIT_WRITE"],
  ];
  return (
    safeReasons.find(([message]) => output.includes(message))?.[1] ?? "UNKNOWN"
  );
}

function classifyPostgreSQLFailure(output) {
  const safeReasons = [
    ["permission denied", "SQL_PERMISSION"],
    ["violates foreign key constraint", "FOREIGN_KEY"],
    ["violates check constraint", "CHECK_CONSTRAINT"],
    ["does not exist", "SCHEMA_MISMATCH"],
    ["already exists", "DUPLICATE_OBJECT"],
    ["null value in column", "NULL_CONSTRAINT"],
  ];
  return (
    safeReasons.find(([message]) => output.includes(message))?.[1] ?? "UNKNOWN"
  );
}

function runCommand(command, args, options, stage) {
  const { failureClassifier, ...spawnOptions } = options;
  const result = spawnSync(command, args, {
    encoding: "utf8",
    maxBuffer: 1024 * 1024,
    timeout: 180_000,
    windowsHide: true,
    ...spawnOptions,
  });
  if (result.error || result.status !== 0) {
    const status = result.status ?? "spawn error";
    const reason = failureClassifier?.(
      `${result.stdout ?? ""}\n${result.stderr ?? ""}`,
    );
    throw new Error(
      `Database fixture ${stage} failed (${status}${reason ? `, ${reason}` : ""}).`,
    );
  }
  return (result.stdout ?? "").trim();
}

function psql(config, database, query, options = {}) {
  const environment = {
    ...maintenanceProcessEnvironment(config, database),
    ...options.environment,
  };
  const args = [
    "--no-psqlrc",
    "--no-password",
    "--quiet",
    "--set=ON_ERROR_STOP=1",
    "--dbname",
    database,
  ];
  if (options.scalar) {
    args.push("--tuples-only", "--no-align");
  }
  if (query !== undefined) {
    args.push("--command", query);
  }
  return runCommand(
    "psql",
    args,
    {
      cwd: projectRoot,
      env: environment,
      input: options.input,
      failureClassifier: options.failureClassifier ?? classifyPostgreSQLFailure,
    },
    options.stage ?? "SQL command",
  );
}

function ensureNoLoginRole(config, state) {
  const roleStatus = psql(
    config,
    "postgres",
    `SELECT CASE WHEN NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '${noLoginRole}') THEN 'missing' WHEN EXISTS (SELECT 1 FROM pg_roles g WHERE g.rolname = '${noLoginRole}' AND (g.rolcanlogin OR g.rolsuper OR g.rolcreatedb OR g.rolcreaterole OR g.rolbypassrls OR g.rolreplication)) THEN 'unsafe' WHEN EXISTS (SELECT 1 FROM pg_roles g JOIN pg_roles member ON pg_has_role(g.oid, member.oid, 'MEMBER') WHERE g.rolname = '${noLoginRole}' AND (member.rolsuper OR member.rolcreatedb OR member.rolcreaterole OR member.rolbypassrls OR member.rolreplication)) THEN 'unsafe' WHEN has_database_privilege('${noLoginRole}', 'postgres', 'CREATE') OR has_schema_privilege('${noLoginRole}', 'public', 'CREATE') THEN 'unsafe' ELSE 'restricted' END;`,
    { scalar: true },
  );
  if (roleStatus === "unsafe") {
    throw new Error("Existing control-plane group has unsafe role attributes.");
  }
  if (roleStatus === "missing") {
    state.attemptedGroup = true;
    psql(config, "postgres", `CREATE ROLE ${noLoginRole} NOLOGIN;`, {
      stage: "NOLOGIN control-plane role creation",
    });
  }
}

function quoteGeneratedIdentifier(value) {
  if (!/^[a-z0-9_]+$/.test(value)) {
    throw new Error("Generated PostgreSQL identifier is invalid.");
  }
  return `"${value}"`;
}

function createRestrictedAppLogin(config, state, appPassword) {
  state.attemptedAppRole = true;
  const appRole = quoteGeneratedIdentifier(state.appRole);
  psql(config, "postgres", undefined, {
    input: [
      "\\getenv app_password TBCG_E2E_APP_ROLE_PASSWORD",
      `CREATE ROLE ${appRole} LOGIN PASSWORD :'app_password';`,
    ].join("\n"),
    environment: { TBCG_E2E_APP_ROLE_PASSWORD: appPassword },
    stage: "restricted app login creation",
  });
  psql(config, "postgres", `GRANT ${noLoginRole} TO ${appRole};`, {
    stage: "restricted app role membership",
  });
}

function validatePostgreSQL18(config) {
  const version = Number(
    psql(
      config,
      "postgres",
      "SELECT current_setting('server_version_num')::integer;",
      { scalar: true, stage: "PostgreSQL version check" },
    ),
  );
  if (!Number.isInteger(version) || version < 180000 || version >= 190000) {
    throw new Error("The maintenance target is not PostgreSQL 18.");
  }
}

function validateTern(config) {
  const version = runCommand(
    ternPath,
    ["version"],
    {
      cwd: serverRoot,
      env: { ...safeProcessEnvironment(), GOTOOLCHAIN: config.toolchain },
    },
    "Tern version check",
  );
  if (!version.includes("v2.4.3")) {
    throw new Error("The migration tool must be Tern v2.4.3.");
  }
}

function migrationEnvironment(config, databaseName, migrationDirectory) {
  return {
    ...maintenanceProcessEnvironment(config, databaseName),
    TERN_MIGRATIONS: migrationDirectory,
    GOTOOLCHAIN: config.toolchain,
  };
}

function runMigrations(config, databaseName) {
  const migrationDirectory = resolve(serverRoot, "migrations");
  const environment = migrationEnvironment(
    config,
    databaseName,
    migrationDirectory,
  );
  runCommand(
    ternPath,
    ["migrate"],
    { cwd: serverRoot, env: environment },
    "Tern migration",
  );
}

function seedGames(config, databaseName) {
  psql(
    config,
    databaseName,
    `INSERT INTO games (game_id, display_name) VALUES ('demo', 'TBCG E2E demo'), ('zz-e2e-scope', 'TBCG E2E scope');`,
    { stage: "test Game fixture seed" },
  );
}

function bootstrapAccount(config, databaseName, account, role) {
  const roleName =
    role === "AUTHOR" ? "Author" : role[0] + role.slice(1).toLowerCase();
  const username = account[`TBCG_ADMIN_E2E_${role}_USERNAME`];
  const password = account[`TBCG_ADMIN_E2E_${role}_PASSWORD`];
  const adminURL = new URL(config.url);
  adminURL.pathname = `/${databaseName}`;
  const environment = {
    ...safeProcessEnvironment(),
    GOTOOLCHAIN: config.toolchain,
    TBCG_ADMIN_BOOTSTRAP_CONFIRM: bootstrapConfirmation,
    TBCG_ADMIN_BOOTSTRAP_DATABASE_URL: adminURL.toString(),
    TBCG_ADMIN_BOOTSTRAP_USERNAME: username,
    TBCG_ADMIN_BOOTSTRAP_PASSWORD: password,
    TBCG_ADMIN_BOOTSTRAP_GAME_ID: "demo",
    TBCG_ADMIN_BOOTSTRAP_ROLES: roleName,
  };
  runCommand(
    "go",
    ["run", "./cmd/admin-bootstrap"],
    {
      cwd: serverRoot,
      env: environment,
      failureClassifier: classifyBootstrapFailure,
    },
    `${role} account bootstrap`,
  );
}

function seedSecondGameAuthor(config, databaseName, authorUsername) {
  const psqlEnvironment = { TBCG_E2E_AUTHOR_USERNAME: authorUsername };
  const insert = [
    "\\getenv author_username TBCG_E2E_AUTHOR_USERNAME",
    `INSERT INTO admin_role_bindings (game_id, admin_id, role) SELECT 'zz-e2e-scope', admin_id, 'Author' FROM admin_users WHERE username = :'author_username';`,
  ].join("\n");
  psql(config, databaseName, undefined, {
    input: insert,
    environment: psqlEnvironment,
    stage: "second Game Author role seed",
  });
  const count = psql(config, databaseName, undefined, {
    input: [
      "\\getenv author_username TBCG_E2E_AUTHOR_USERNAME",
      `SELECT count(*) FROM admin_role_bindings r JOIN admin_users u USING (admin_id) WHERE r.game_id = 'zz-e2e-scope' AND r.role = 'Author' AND u.username = :'author_username';`,
    ].join("\n"),
    environment: psqlEnvironment,
    scalar: true,
    stage: "second Game Author role verification",
  });
  if (count !== "1") {
    throw new Error(
      "Second Game Author role seed did not create exactly one binding.",
    );
  }
}

function writePrivateBusinessEnvironment(directory, environment) {
  mkdirSync(directory, { recursive: true, mode: 0o700 });
  const path = resolve(directory, "admin-e2e-env.json");
  writeFileSync(path, JSON.stringify(environment), { flag: "wx", mode: 0o600 });
  if ((statSync(path).mode & 0o077) !== 0) {
    throw new Error("Generated business environment JSON is not private.");
  }
  return path;
}

function cleanupFixture(config, state) {
  if (state.cleaned) return;
  const failures = [];
  if (state.attemptedDatabase) {
    try {
      psql(
        config,
        "postgres",
        `DROP DATABASE IF EXISTS ${quoteGeneratedIdentifier(state.databaseName)} WITH (FORCE);`,
        { stage: "owned database cleanup" },
      );
    } catch {
      failures.push("app database");
    }
  }
  if (state.attemptedAppRole) {
    try {
      psql(
        config,
        "postgres",
        `DROP ROLE IF EXISTS ${quoteGeneratedIdentifier(state.appRole)};`,
        { stage: "app role cleanup" },
      );
    } catch {
      failures.push("app login role");
    }
  }
  if (state.attemptedGroup) {
    try {
      psql(config, "postgres", `DROP ROLE IF EXISTS ${noLoginRole};`, {
        stage: "owned NOLOGIN role cleanup",
      });
    } catch {
      failures.push("owned NOLOGIN role");
    }
  }
  if (failures.length > 0) {
    throw new Error(
      `Unable to clean up owned E2E database resources: ${failures.join(", ")}.`,
    );
  }
  state.cleaned = true;
}

export function createAdminDatabaseFixture(
  maintenanceEnvironmentPath,
  privateDirectory,
) {
  const config = readMaintenanceEnvironment(maintenanceEnvironmentPath);
  const state = {
    databaseName: randomIdentifier("tbcg_e2e_"),
    appRole: randomIdentifier("tbcg_admin_e2e_"),
    attemptedGroup: false,
    attemptedAppRole: false,
    attemptedDatabase: false,
    cleaned: false,
  };
  const accounts = {
    ...credentials("READER"),
    ...credentials("AUTHOR"),
    ...credentials("REVIEWER"),
    ...credentials("PUBLISHER"),
  };
  const appPassword = randomBytes(32).toString("hex");
  let stage = "maintenance target validation";
  try {
    validatePostgreSQL18(config);
    validateTern(config);
    stage = "control-plane group preparation";
    ensureNoLoginRole(config, state);
    stage = "restricted app login preparation";
    createRestrictedAppLogin(config, state, appPassword);
    stage = "random app database creation";
    state.attemptedDatabase = true;
    psql(
      config,
      "postgres",
      `CREATE DATABASE ${quoteGeneratedIdentifier(state.databaseName)};`,
      { stage },
    );
    stage = "Tern migration";
    runMigrations(config, state.databaseName);
    stage = "test Game fixture seed";
    seedGames(config, state.databaseName);
    for (const role of ["READER", "AUTHOR", "REVIEWER", "PUBLISHER"]) {
      stage = `controlled bootstrap ${role} account`;
      bootstrapAccount(config, state.databaseName, accounts, role);
    }
    stage = "second Game Author role seed";
    seedSecondGameAuthor(
      config,
      state.databaseName,
      accounts.TBCG_ADMIN_E2E_AUTHOR_USERNAME,
    );
    const appURL = new URL(config.url);
    appURL.pathname = `/${state.databaseName}`;
    appURL.username = state.appRole;
    appURL.password = appPassword;
    const businessEnvironment = {
      TBCG_ADMIN_DATABASE_URL: appURL.toString(),
      ...accounts,
    };
    const businessEnvironmentPath = writePrivateBusinessEnvironment(
      privateDirectory,
      businessEnvironment,
    );
    return {
      databaseName: state.databaseName,
      appRole: state.appRole,
      businessEnvironmentPath,
      cleanup: () => cleanupFixture(config, state),
    };
  } catch (error) {
    const reason =
      error instanceof Error ? error.message : "Unexpected setup error.";
    try {
      cleanupFixture(config, state);
    } catch {
      throw new Error(
        `E2E database setup failed during ${stage}: ${reason}; owned-resource cleanup also failed.`,
      );
    }
    throw new Error(
      `E2E database setup failed during ${stage}: ${reason}; owned resources were cleaned.`,
    );
  }
}
