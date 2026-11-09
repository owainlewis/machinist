export class SignedOutError extends Error {
  constructor() { super("Sign in to continue."); this.name = "SignedOutError"; }
}

export async function api(path, { method = "GET", body } = {}) {
  const response = await fetch(path, {
    method,
    headers: { Accept: "application/json", ...(body === undefined ? {} : { "Content-Type": "application/json" }) },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });
  if (response.status === 401) throw new SignedOutError();
  if (!response.ok) {
    const failure = await response.json().catch(() => ({}));
    throw new Error(failure.error || `Request failed (${response.status})`);
  }
  return response.status === 204 ? null : response.json();
}
