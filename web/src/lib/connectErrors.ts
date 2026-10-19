import { ApiError } from "./api";

// What the user is told when connecting to Rivian fails, by the class the server sends (user_action, transient, outage,
// rate_limited, unknown). The server's own text is never shown as is: for anything but a rejected password it is the upstream
// message, which can read like a Rivolt bug. `track` is the failure class for the event (the server's allowed values).
export type ConnectFailure = {
  cls: "user_action" | "transient" | "outage" | "rate_limited" | "unknown";
  title: string;
  body: string;
  track: "user-action" | "rate" | "mfa" | "other";
};

const classOf = (err: unknown): ConnectFailure["cls"] => {
  const c = err instanceof ApiError && err.body && typeof err.body === "object" ? (err.body as { class?: unknown }).class : undefined;
  return c === "user_action" || c === "transient" || c === "outage" || c === "rate_limited" ? c : "unknown";
};

export function connectFailure(err: unknown, step: "password" | "code"): ConnectFailure {
  const cls = classOf(err);
  if (cls === "rate_limited") {
    return {
      cls,
      title: "Too many tries",
      body: "Rivian pauses sign-ins after several attempts. Wait about 10 minutes, then try once more; your details are fine as long as they match the Rivian app.",
      track: "rate",
    };
  }
  if (cls === "transient" || cls === "outage") {
    return {
      cls,
      title: "Rivian isn't answering right now",
      body: "This isn't a problem with your details. Rivian's sign-in is slow or down; try again in a few minutes.",
      track: "other",
    };
  }
  if (step === "code") {
    return { cls, title: "That code didn't work", body: "Codes expire after a few minutes. Ask Rivian for a new one and enter the newest code you receive.", track: "mfa" };
  }
  if (cls === "user_action") {
    return {
      cls,
      title: "Rivian didn't accept that sign-in",
      body: "Check the email and password; they're the ones you use in the Rivian app. If this is a driver account, the driver has to accept your invite and set their own password in the Rivian app first.",
      track: "user-action",
    };
  }
  return { cls, title: "Couldn't sign in", body: "Something went wrong on the way to Rivian. Try again in a few minutes.", track: "other" };
}
