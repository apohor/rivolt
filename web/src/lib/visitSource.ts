// Where a visit came from, as the two tokens the events allowlist takes:
// the referrer's host and utm_source. Never a full URL, never other query
// parameters.

const TOKEN = /^[a-z0-9_.-]{1,40}$/;

function tok(v: string | null | undefined): string | undefined {
  const s = v?.toLowerCase();
  return s && TOKEN.test(s) ? s : undefined;
}

export function visitSourceProps(search: string, referrer: string): Record<string, string> {
  const props: Record<string, string> = {};
  let host: string | undefined;
  try {
    host = referrer ? tok(new URL(referrer).hostname) : undefined;
  } catch {
    host = undefined;
  }
  if (host) props.referrer_host = host;
  const utm = tok(new URLSearchParams(search).get("utm_source"));
  if (utm) props.utm_source = utm;
  return props;
}
