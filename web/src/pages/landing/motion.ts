// True when the visitor asked for reduced motion, or the browser cannot say
// (then the page shows its resting state, which is the full content).
export const reducedMotion = () => typeof matchMedia !== "function" || matchMedia("(prefers-reduced-motion: reduce)").matches;
