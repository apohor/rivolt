// webglAvailable reports whether this browser can create a WebGL context. A privacy browser, an old device or a blocked GPU cannot, and the map
// must then say "map unavailable" rather than leave a blank or broken card. The probe is cached: a context is not free.
let cached: boolean | undefined;

export function webglAvailable(doc: Pick<Document, "createElement"> = document): boolean {
  if (cached !== undefined) return cached;
  cached = probe(doc);
  return cached;
}

export function probe(doc: Pick<Document, "createElement">): boolean {
  try {
    const canvas = doc.createElement("canvas") as HTMLCanvasElement;
    const gl = canvas.getContext("webgl2") ?? canvas.getContext("webgl");
    if (!gl) return false;
    // Release the probe's context at once; a page keeps only a handful.
    (gl as WebGLRenderingContext).getExtension("WEBGL_lose_context")?.loseContext();
    return true;
  } catch {
    return false;
  }
}

// resetWebGLProbe forgets the cached answer (tests).
export function resetWebGLProbe(): void {
  cached = undefined;
}
