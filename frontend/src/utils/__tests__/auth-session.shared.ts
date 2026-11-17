// Shared pieces for the auth session tests (cookie attributes, logout).
export function recordCookieWrites() {
  const writes: string[] = [];
  Object.defineProperty(document, "cookie", {
    configurable: true,
    get: () => "",
    set: (v: string) => {
      writes.push(v);
    },
  });
  return writes;
}

const b64url = (o: object) =>
  btoa(JSON.stringify(o))
    .replace(/=+$/, "")
    .replace(/\+/g, "-")
    .replace(/\//g, "_");

export const fakeJwt = () =>
  `${b64url({ alg: "HS256" })}.${b64url({
    exp: Math.floor(Date.now() / 1000) + 3600,
    user: { id: 1, locale: "en", perm: {} },
  })}.sig`;
