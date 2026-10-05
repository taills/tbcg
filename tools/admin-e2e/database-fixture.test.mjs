import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import test from "node:test";
import { parseMaintenanceEnvironment } from "./database-fixture.mjs";

const confirmation = "I_UNDERSTAND_TEST_DB_IS_DISPOSABLE";
const caCertificatePath = "/tmp/e2e-test-ca.crt";

function maintenanceEnvironment(overrides = {}) {
  const url = new URL(
    "postgresql://127.0.0.1:55432/postgres?sslmode=disable&sslrootcert=%2Ftmp%2Funtrusted-ca.crt",
  );
  url.username = `e2e_${randomUUID().replaceAll("-", "")}`;
  url.password = randomUUID();
  return {
    TBCG_TEST_DATABASE_URL: url.toString(),
    TBCG_TEST_DATABASE_CONFIRM: confirmation,
    TBCG_REQUIRE_TEST_DATABASE: "1",
    GOTOOLCHAIN: "go1.26.4",
    root: "not-an-environment-variable",
    ...overrides,
  };
}

test("accepts only the approved disposable PostgreSQL maintenance target", () => {
  const config = parseMaintenanceEnvironment(
    maintenanceEnvironment(),
    {},
    caCertificatePath,
  );
  assert.equal(config.host, "127.0.0.1");
  assert.equal(config.port, 55432);
  assert.equal(config.database, "postgres");
  assert.equal(config.toolchain, "go1.26.4");
  assert.equal(config.parameters.get("sslmode"), "verify-full");
  assert.equal(config.parameters.get("sslrootcert"), caCertificatePath);
  assert.equal(config.root, undefined);
});

test("rejects missing destructive-operation confirmation", () => {
  assert.throws(
    () =>
      parseMaintenanceEnvironment(
        maintenanceEnvironment({ TBCG_TEST_DATABASE_CONFIRM: "" }),
        {},
      ),
    /confirmation/,
  );
});

test("rejects any database outside the dedicated loopback test target", () => {
  const unsafeURL = new URL(maintenanceEnvironment().TBCG_TEST_DATABASE_URL);
  unsafeURL.port = "5432";
  assert.throws(
    () =>
      parseMaintenanceEnvironment(
        maintenanceEnvironment({
          TBCG_TEST_DATABASE_URL: unsafeURL.toString(),
        }),
        {},
      ),
    /port/,
  );
});

test("rejects inherited libpq connection overrides", () => {
  assert.throws(
    () =>
      parseMaintenanceEnvironment(maintenanceEnvironment(), {
        PGSERVICE: "unexpected-service",
      }),
    /PGSERVICE/,
  );
});
