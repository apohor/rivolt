// ApplyProfileChange renders a single "Apply: …" button that fetches
// the linked profile, applies one change, and POSTs it back to the
// machine's /profile/save endpoint. Shared between the full shot
// analysis (which may list several recipe changes) and the coach's
// next-pull suggestion (exactly one change). Kept small and dumb on
// purpose: the caller owns the decision of whether a change is safe
// to apply.
//
// Three kinds of change are supported, matching the directive grammar
// the analyser prompt teaches the LLM:
//
//   - set_variable:        scalar write to profile.variables[<key>].value
//   - remove_exit_trigger: drop every exit_trigger of a given type on a
//                          named stage (idempotent — a no-op if already
//                          absent, so a second click doesn't error)
//   - set_exit_trigger:    update the first matching exit_trigger's value
//                          on a named stage, inserting one if the stage
//                          currently has no matching trigger
//
// Stage and trigger matching is case-insensitive so the model doesn't
// need to reproduce the profile's exact capitalisation.
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { machine, type ExitTrigger, type Profile, type Stage } from "../lib/api";

export type ApplyAction =
	| { kind: "set_variable"; variableKey: string; value: number }
	| { kind: "remove_exit_trigger"; stageName: string; triggerType: string }
	| {
			kind: "set_exit_trigger";
			stageName: string;
			triggerType: string;
			value: number;
	  };

type Props = {
	profileId?: string;
	action: ApplyAction;
};

export default function ApplyProfileChange({ profileId, action }: Props) {
	const qc = useQueryClient();
	const apply = useMutation<void, Error, void>({
		mutationFn: async () => {
			if (!profileId) throw new Error("no profile id on shot");
			const current: Profile = await machine.getProfile(profileId);
			const next = applyAction(current, action);
			await machine.saveProfile(next);
			await qc.invalidateQueries({ queryKey: ["profile", profileId] });
			await qc.invalidateQueries({ queryKey: ["profiles"] });
		},
	});

	const disabled = !profileId || apply.isPending || apply.isSuccess;
	const label = apply.isSuccess
		? "Applied"
		: apply.isPending
			? "Applying…"
			: describeAction(action);
	const tooltip = profileId
		? tooltipFor(action)
		: "No linked profile on this shot";

	return (
		<div className="flex flex-wrap items-center gap-2">
			<button
				onClick={() => apply.mutate()}
				disabled={disabled}
				title={tooltip}
				className="inline-flex items-center gap-1.5 rounded-md border border-emerald-700 bg-emerald-900/40 px-2.5 py-1 font-mono text-[11px] text-emerald-100 hover:bg-emerald-900/60 disabled:cursor-not-allowed disabled:opacity-60"
			>
				{apply.isSuccess ? <span aria-hidden>✓</span> : <span aria-hidden>↻</span>}
				{label}
			</button>
			{apply.error && (
				<span className="text-[11px] text-rose-300">
					{String(apply.error.message || apply.error)}
				</span>
			)}
		</div>
	);
}

// describeAction renders a short, scannable label for the button face.
function describeAction(a: ApplyAction): string {
	switch (a.kind) {
		case "set_variable":
			return `Apply: ${a.variableKey} = ${a.value}`;
		case "remove_exit_trigger":
			return `Apply: remove ${a.triggerType} trigger from "${a.stageName}"`;
		case "set_exit_trigger":
			return `Apply: ${a.triggerType} trigger = ${a.value} on "${a.stageName}"`;
	}
}

function tooltipFor(a: ApplyAction): string {
	switch (a.kind) {
		case "set_variable":
			return `Set ${a.variableKey} to ${a.value} on the profile and save to the machine`;
		case "remove_exit_trigger":
			return `Remove all ${a.triggerType} exit_triggers from stage "${a.stageName}" and save to the machine`;
		case "set_exit_trigger":
			return `Set the ${a.triggerType} exit_trigger to ${a.value} on stage "${a.stageName}" and save to the machine`;
	}
}

// applyAction returns a new profile with the change applied. Pure so
// the mutation is easy to reason about.
function applyAction(profile: Profile, action: ApplyAction): Profile {
	switch (action.kind) {
		case "set_variable": {
			const vars = Array.isArray(profile.variables) ? profile.variables : [];
			const idx = vars.findIndex((v) => v.key === action.variableKey);
			if (idx < 0) {
				throw new Error(`variable "${action.variableKey}" not found in profile`);
			}
			const next = vars.slice();
			next[idx] = { ...next[idx], value: action.value };
			return { ...profile, variables: next };
		}
		case "remove_exit_trigger": {
			return mutateStage(profile, action.stageName, (stage) => {
				const triggers = Array.isArray(stage.exit_triggers)
					? stage.exit_triggers
					: [];
				const kept = triggers.filter(
					(t) => !typeMatches(t.type, action.triggerType),
				);
				// Idempotent: if nothing matched, leave the stage alone
				// rather than asserting — makes double-click safe.
				return { ...stage, exit_triggers: kept };
			});
		}
		case "set_exit_trigger": {
			return mutateStage(profile, action.stageName, (stage) => {
				const triggers: ExitTrigger[] = Array.isArray(stage.exit_triggers)
					? stage.exit_triggers.slice()
					: [];
				const idx = triggers.findIndex((t) =>
					typeMatches(t.type, action.triggerType),
				);
				if (idx >= 0) {
					triggers[idx] = { ...triggers[idx], value: action.value };
				} else {
					triggers.push({ type: action.triggerType, value: action.value });
				}
				return { ...stage, exit_triggers: triggers };
			});
		}
	}
}

function mutateStage(
	profile: Profile,
	stageName: string,
	fn: (s: Stage) => Stage,
): Profile {
	const stages = Array.isArray(profile.stages) ? profile.stages : [];
	const idx = stages.findIndex((s) => nameMatches(s.name, stageName));
	if (idx < 0) {
		const available = stages.map((s) => `"${s.name}"`).join(", ") || "(none)";
		throw new Error(
			`stage "${stageName}" not found in profile; available: ${available}`,
		);
	}
	const next = stages.slice();
	next[idx] = fn(next[idx]);
	return { ...profile, stages: next };
}

function nameMatches(a: string | undefined, b: string): boolean {
	return (a ?? "").trim().toLowerCase() === b.trim().toLowerCase();
}

function typeMatches(a: string | undefined, b: string): boolean {
	return (a ?? "").trim().toLowerCase() === b.trim().toLowerCase();
}
