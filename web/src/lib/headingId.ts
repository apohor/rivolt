// The id a legal page gives a "##" heading so /terms#2 and /support#free-and-pro land on the section. A heading that starts with a number
// ("2. What Rivolt is") keeps the id it always had, the bare number (every existing /terms#N deep link). Any other heading gets a slug of
// its text: lower case, accents folded, apostrophes dropped, runs of anything else become one hyphen ("Free and Pro" -> "free-and-pro").
// Nothing to anchor to (an empty slug) gives no id.
export function headingId(text: string): string | undefined {
  const num = text.match(/^(\d+)\./);
  if (num) return num[1];
  const slug = text
    .normalize("NFKD")
    .replace(/[̀-ͯ]/g, "")
    .toLowerCase()
    .replace(/['‘’]/g, "")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
  return slug || undefined;
}
