// With BASE_URL and E2E_ADMIN_PRIVATE_KEY, log in once and share the session
// through E2E_ADMIN_SESSION. Parallel logins can replace each other's challenges
// and hit auth rate limits.
import { request } from "@playwright/test";
import { privateKeyToAccount } from "viem/accounts";

export default async function globalSetup(): Promise<void> {
  const baseURL = process.env.BASE_URL;
  const key = process.env.E2E_ADMIN_PRIVATE_KEY as `0x${string}` | undefined;
  if (!baseURL || !key) return;

  const account = privateKeyToAccount(key);
  const api = await request.newContext({ baseURL });
  try {
    const challenge = await api.post("/auth/challenge", {
      data: { address: account.address },
    });
    if (!challenge.ok()) {
      throw new Error(`POST /auth/challenge failed: ${challenge.status()}`);
    }
    const { nonce, message } = (await challenge.json()) as {
      nonce: string;
      message: string;
    };
    const signature = await account.signMessage({ message });
    const session = await api.post("/auth/session", {
      data: { address: account.address, nonce, signature },
    });
    if (!session.ok()) {
      throw new Error(`POST /auth/session failed: ${session.status()}`);
    }
    process.env.E2E_ADMIN_SESSION = JSON.stringify(await session.json());
  } finally {
    await api.dispose();
  }
}
