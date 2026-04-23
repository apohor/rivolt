// Parses the LLM's markdown critique into structured sections we can
// render as scannable UI instead of a wall of markdown.
//
// Expected shape (enforced by analyzer.go's systemPrompt after the RATING
// line has already been stripped):
//
//   <short tldr paragraph>
//
//   ## What the numbers say
//   - **Pressure:** ...
//   - **Flow:** ...
//
//   ## Suggestions
//   1. **Grind coarser** — ...
//   2. ...
//
// Parsing is tolerant: missing sections fall back to an empty array, and
// the caller can decide to render the raw markdown if nothing matched.

export type ParsedObservation = {
  /** Short bold label at the start of the bullet, e.g. "Pressure". "" if absent. */
  label: string;
  /** Remainder of the bullet after the label, in raw markdown. */
  body: string;
};

// RecipeChange is a single "## Recipe changes" bullet with an optional
// machine-parsable directive peeled off so the UI can render an
// "Apply" button next to it. A bullet can carry either a scalar
// variable write or a structural stage-level edit (not both — the
// prompt tells the model to emit one directive per bullet).
export type RecipeChange = {
	/** Human-readable prose with the directive line stripped out. */
	body: string;
	/** The `key` of a profile variable to update. Undefined when the
	 *  model didn't emit a machine-parsable directive for this bullet. */
	variableKey?: string;
	/** New value to write into the variable. Only set with variableKey. */
	value?: number;
	/** A structural edit to one of the profile's stages (currently:
	 *  add/remove/update an exit_trigger). Mutually exclusive with
	 *  variableKey — the first matching directive wins. */
	stageOp?: StageExitTriggerOp;
};

// StageExitTriggerOp is a structural edit against a named stage's
// exit_triggers[] array. We match the stage case-insensitively by
// name and match the trigger case-insensitively by type.
export type StageExitTriggerOp =
	| {
			kind: "remove_exit_trigger";
			stageName: string;
			triggerType: string;
	  }
	| {
			kind: "set_exit_trigger";
			stageName: string;
			triggerType: string;
			value: number;
	  };
export type ParsedAnalysis = {
  /** Everything before the first `##` heading. */
  tldr: string;
  observations: ParsedObservation[];
  /** Off-machine preparation tips (grind, dose, tamp, technique). */
  preparation: string[];
  /** Profile/recipe tweaks. Each may carry a machine-apply directive. */
  recipe: RecipeChange[];
  /** Legacy "Suggestions" section when the model ignored the new
   *  Preparation/Recipe split. Empty when preparation or recipe parsed. */
  suggestions: string[];
  /** True when we found at least one structured section. Callers that
   *  get `false` should fall back to rendering the raw markdown. */
  ok: boolean;
};

const HEADING_RE = /^\s*##+\s+(.+?)\s*$/;

export function parseAnalysis(markdown: string): ParsedAnalysis {
  const lines = markdown.split("\n");
  const sections: { name: string; body: string[] }[] = [
    { name: "__tldr__", body: [] },
  ];
  for (const line of lines) {
    const m = line.match(HEADING_RE);
    if (m) {
      sections.push({ name: m[1].toLowerCase(), body: [] });
    } else {
      sections[sections.length - 1].body.push(line);
    }
  }

  const tldr = (sections.find((s) => s.name === "__tldr__")?.body ?? [])
    .join("\n")
    .trim();

  const findSection = (needle: RegExp): string[] => {
    const sec = sections.find((s) => s.name !== "__tldr__" && needle.test(s.name));
    return sec ? sec.body : [];
  };

  const numbersBody = findSection(/numbers|observ|analys|what|data/);
  // Preparation / Recipe first — if those exist we prefer them over
  // the old flat "Suggestions" section. We still parse "Suggestions"
  // as a fallback so older cached analyses render.
  const preparationBody = findSection(/prep|barista|technique|off.?machine/);
  const recipeBody = findSection(/recipe|profile|variable|tweak/);
  const suggestionsBody = findSection(/^suggest|recommend|advic|fix|action|improve/);

  const observations = parseBullets(numbersBody).map(parseObservation);
  const preparation = parseBullets(preparationBody);
  const recipe = parseBullets(recipeBody).map(parseRecipeChange);
  // Don't double-render: if the newer sections were present, ignore
  // the legacy flat Suggestions block.
  const suggestions =
    preparation.length === 0 && recipe.length === 0
      ? parseBullets(suggestionsBody)
      : [];

  return {
    tldr,
    observations,
    preparation,
    recipe,
    suggestions,
    ok:
      observations.length > 0 ||
      preparation.length > 0 ||
      recipe.length > 0 ||
      suggestions.length > 0,
  };
}

// parseBullets splits a slab of markdown into individual list items,
// handling both `- ` and `1.` style lists and multi-line continuations
// (indented wraps become part of the preceding item).
function parseBullets(lines: string[]): string[] {
  const items: string[] = [];
  const bulletStart = /^\s*(?:[-*]|\d+[.)])\s+(.*)$/;
  for (const raw of lines) {
    const line = raw.replace(/\s+$/, "");
    if (!line.trim()) continue;
    const m = line.match(bulletStart);
    if (m) {
      items.push(m[1]);
    } else if (items.length > 0) {
      // Continuation of previous item (indented or wrapped).
      items[items.length - 1] += " " + line.trim();
    }
    // Lines outside any bullet (e.g. a stray paragraph under the heading)
    // are intentionally dropped — the model is supposed to use a list.
  }
  return items;
}

// parseObservation peels off a leading bold label like `**Pressure:**` so
// the UI can render it as a pill. Handles `**Label:**`, `**Label**:`,
// and `**Label**` with or without trailing punctuation.
const LABEL_RE = /^\s*\*\*([^*]+?)\*\*\s*[:\-—–]?\s*(.*)$/;
function parseObservation(text: string): ParsedObservation {
  const m = text.match(LABEL_RE);
  if (!m) return { label: "", body: text };
  return { label: m[1].replace(/[:\s]+$/, "").trim(), body: m[2].trim() };
}

// parseRecipeChange pulls a machine-parsable directive out of a recipe
// bullet. Three directive forms are supported, any of which may appear
// inline, on their own line, or wrapped in ``/```` fences:
//
//     SET variable <key> = <number>
//     REMOVE exit_trigger <type> FROM stage "<name>"
//     SET exit_trigger <type> = <number> ON stage "<name>"
//
// Structural directives are tried before the scalar SET because the
// scalar regex requires the literal word "variable" and so won't eat
// them, but ordering keeps intent obvious. The <key> in SET variable
// may contain spaces (real Meticulous variable keys include things
// like "time_Decline Duration") so we grab everything between the
// keyword and the assignment operator rather than constraining to
// word characters. Returns a RecipeChange with the directive (and any
// stray surrounding backticks) removed from `body` so the UI doesn't
// show the same text twice.
const SET_VARIABLE_RE =
	/(?:`{1,3})?\s*set\s+variable\s+([^\n`]+?)\s*(?:=|\bto\b|:)\s*(-?\d+(?:\.\d+)?)\s*(?:`{1,3})?/i;
// Stage-name capture: prefer a double-quoted name ("Pressure Decline")
// and fall back to a bare word (decline). Trigger-type is a single
// word (flow, pressure, time, weight, etc).
const REMOVE_TRIGGER_RE =
	/(?:`{1,3})?\s*remove\s+exit[_\s]?trigger\s+(\w+)\s+from\s+stage\s+(?:"([^"\n]+)"|'([^'\n]+)'|(\w+))\s*(?:`{1,3})?/i;
const SET_TRIGGER_RE =
	/(?:`{1,3})?\s*set\s+exit[_\s]?trigger\s+(\w+)\s*(?:=|\bto\b|:)\s*(-?\d+(?:\.\d+)?)\s+on\s+stage\s+(?:"([^"\n]+)"|'([^'\n]+)'|(\w+))\s*(?:`{1,3})?/i;

function parseRecipeChange(text: string): RecipeChange {
	// 1) REMOVE exit_trigger <type> FROM stage "<name>"
	{
		const m = text.match(REMOVE_TRIGGER_RE);
		if (m) {
			const triggerType = m[1].trim();
			const stageName = (m[2] ?? m[3] ?? m[4] ?? "").trim();
			if (stageName && triggerType) {
				return {
					body: cleanupBody(sliceOut(text, m)),
					stageOp: {
						kind: "remove_exit_trigger",
						stageName,
						triggerType,
					},
				};
			}
		}
	}
	// 2) SET exit_trigger <type> = <number> ON stage "<name>"
	{
		const m = text.match(SET_TRIGGER_RE);
		if (m) {
			const triggerType = m[1].trim();
			const value = Number(m[2]);
			const stageName = (m[3] ?? m[4] ?? m[5] ?? "").trim();
			if (stageName && triggerType && Number.isFinite(value)) {
				return {
					body: cleanupBody(sliceOut(text, m)),
					stageOp: {
						kind: "set_exit_trigger",
						stageName,
						triggerType,
						value,
					},
				};
			}
		}
	}
	// 3) SET variable <key> = <number>
	{
		const m = text.match(SET_VARIABLE_RE);
		if (m) {
			const key = m[1].trim();
			const value = Number(m[2]);
			if (Number.isFinite(value) && key.length > 0) {
				return {
					body: cleanupBody(sliceOut(text, m)),
					variableKey: key,
					value,
				};
			}
		}
	}
	return { body: cleanupBody(text) };
}

// sliceOut returns `text` with the matched directive removed, keeping
// the surrounding prose so the UI can still show the human rationale.
function sliceOut(text: string, m: RegExpMatchArray): string {
	const idx = m.index ?? 0;
	return text.slice(0, idx) + " " + text.slice(idx + m[0].length);
}

// cleanupBody tidies up a recipe-change bullet after the directive has
// been sliced out. Models sometimes wrap the directive in a fenced code
// block, so once we've removed "SET variable foo = 1" the leftover text
// contains stray ``` or ` runs and dangling punctuation. This is purely
// cosmetic — the Apply button still works regardless.
function cleanupBody(s: string): string {
	return s
		// Collapse any leftover code-fence runs of 3+ backticks.
		.replace(/`{3,}/g, "")
		// Empty inline-code spans `` and lone stray backticks.
		.replace(/``+/g, "")
		// Leading/trailing em-dashes or hyphens left behind by the slice.
		.replace(/\s*[—–-]+\s*$/, "")
		.replace(/^\s*[—–-]+\s*/, "")
		// Multiple spaces → one.
		.replace(/\s{2,}/g, " ")
		.trim();
}
