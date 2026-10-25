import { expect, test, type Page } from "@playwright/test";
import { stubApi } from "./stub";

// Passkeys (server: #1069). The browser side runs against a stubbed server and Chromium's virtual authenticator: Settings adds a
// passkey (asking for the password again), the Hydra login page signs in with it, and neither shows where passkeys are off.
const b64url = (s: string) => Buffer.from(s).toString("base64url");

async function authenticator(page: Page) {
  const cdp = await page.context().newCDPSession(page);
  await cdp.send("WebAuthn.enable");
  await cdp.send("WebAuthn.addVirtualAuthenticator", {
    options: { protocol: "ctap2", transport: "internal", hasResidentKey: true, hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true },
  });
}

test("adds a passkey from Settings, then signs in with it on the login page", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();
  await authenticator(page);
  await stubApi(page);
  const rpId = "localhost";
  const list: { id: string; label: string; created_at: string; last_used_at: string | null; synced: boolean }[] = [];
  const sent: Record<string, unknown> = {};
  await page.route("**/api/auth/passkeys", (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ passkeys: list, max: 10 }) }));
  await page.route("**/api/auth/passkeys/register/begin", (r) => {
    sent.password = (r.request().postDataJSON() as { password: string }).password;
    return r.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        ceremony_id: "c1",
        options: {
          publicKey: {
            rp: { id: rpId, name: "Rivolt" },
            user: { id: b64url("user-handle-1"), name: "owner@example.com", displayName: "Owner" },
            challenge: b64url("register-challenge"),
            pubKeyCredParams: [{ type: "public-key", alg: -7 }],
            authenticatorSelection: { residentKey: "required", requireResidentKey: true, userVerification: "required" },
            timeout: 60000,
          },
        },
      }),
    });
  });
  await page.route("**/api/auth/passkeys/register/finish", (r) => {
    sent.finish = r.request().postDataJSON();
    list.push({ id: "p1", label: (sent.finish as { label: string }).label, created_at: "2026-10-25T12:00:00Z", last_used_at: null, synced: false });
    return r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ passkey: list[0] }) });
  });

  await page.goto("/settings#passkeys");
  const card = page.locator("[data-passkeys]");
  await expect(card).toContainText("No passkeys yet.");
  await card.getByRole("button", { name: "Add a passkey" }).click();
  await card.getByLabel(/Name/).fill("MacBook");
  await card.getByLabel(/Your password/).fill("correct horse");
  await card.getByRole("button", { name: "Add passkey" }).click();
  await expect(card.locator("[data-passkey-row]")).toContainText("MacBook");
  await expect(card.locator("[data-passkeys-note]")).toContainText("Passkey added");
  expect(sent.password).toBe("correct horse");
  const fin = sent.finish as { ceremony_id: string; label: string; credential: { id: string; type: string; response: { attestationObject: string; clientDataJSON: string } } };
  expect(fin.ceremony_id).toBe("c1");
  expect(fin.credential.type).toBe("public-key");
  expect(fin.credential.response.attestationObject.length).toBeGreaterThan(20);
  // The password is not kept in the form after the attempt.
  await expect(card.getByLabel(/Your password/)).toHaveCount(0);

  // Sign in with it on the Hydra login page.
  await page.route("**/api/auth/hydra/login?*", (r) =>
    r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ challenge: "ch1", client_id: "grafana", client_name: "Grafana", requested_scope: ["openid"], passkeys: true }) }),
  );
  await page.route("**/api/auth/hydra/passkey/begin", (r) => {
    sent.hydraBegin = r.request().postDataJSON();
    return r.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ ceremony_id: "c2", options: { publicKey: { challenge: b64url("login-challenge"), rpId, userVerification: "required", timeout: 60000 } } }),
    });
  });
  await page.route("**/api/auth/hydra/passkey/finish", (r) => {
    sent.hydraFinish = r.request().postDataJSON();
    return r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ redirect_to: "/signed-in-landing" }) });
  });
  await page.goto("/auth/hydra/login?login_challenge=ch1");
  await page.locator("[data-passkey-signin]").click();
  await expect(page).toHaveURL(/\/signed-in-landing/);
  expect(sent.hydraBegin).toEqual({ challenge: "ch1" });
  const done = sent.hydraFinish as { challenge: string; ceremony_id: string; credential: { id: string; response: { authenticatorData: string; signature: string } } };
  expect(done.challenge).toBe("ch1");
  expect(done.ceremony_id).toBe("c2");
  expect(done.credential.id).toBe(fin.credential.id);
  expect(done.credential.response.signature.length).toBeGreaterThan(20);
  await ctx.close();
});

test("a wrong password and a refused account get plain words, and nothing shows where passkeys are off", async ({ browser }) => {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 900 } });
  const page = await ctx.newPage();
  await authenticator(page);
  await stubApi(page);
  await page.route("**/api/auth/passkeys", (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ passkeys: [], max: 10 }) }));
  let answer: { status: number; body: unknown } = { status: 403, body: { error: "password_incorrect" } };
  await page.route("**/api/auth/passkeys/register/begin", (r) => r.fulfill({ status: answer.status, contentType: "application/json", body: JSON.stringify(answer.body) }));
  await page.goto("/settings#passkeys");
  const card = page.locator("[data-passkeys]");
  await card.getByRole("button", { name: "Add a passkey" }).click();
  await card.getByLabel(/Your password/).fill("nope");
  await card.getByRole("button", { name: "Add passkey" }).click();
  await expect(card.locator("[data-passkeys-note]")).toHaveText("That password isn't right.");
  answer = { status: 409, body: { error: "passkeys_need_an_email_account" } };
  await card.getByLabel(/Your password/).fill("x");
  await card.getByRole("button", { name: "Add passkey" }).click();
  await expect(card.locator("[data-passkeys-note]")).toContainText("Google, GitHub or Apple");
  await expect(card.locator("[data-passkeys-note]")).not.toContainText("passkeys_need_an_email_account");

  // Off: the list route is absent (404) and the login GET has no flag.
  await page.unroute("**/api/auth/passkeys");
  await page.route("**/api/auth/passkeys", (r) => r.fulfill({ status: 404, body: "not found" }));
  await page.reload();
  await expect(page.locator("[data-passkeys]")).toHaveCount(0);
  await page.route("**/api/auth/hydra/login?*", (r) => r.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ challenge: "ch1", client_id: "grafana", requested_scope: [] }) }));
  await page.goto("/auth/hydra/login?login_challenge=ch1");
  await expect(page.getByRole("button", { name: "Sign in" })).toBeVisible();
  await expect(page.locator("[data-passkey-signin]")).toHaveCount(0);
  await ctx.close();
});
