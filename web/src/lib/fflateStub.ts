// Stands in for fflate (vite.config.ts resolve.alias). pmtiles calls decompressSync only when the browser has no DecompressionStream,
// which every browser at or above the supported floor has.
export function decompressSync(): never {
  throw new Error("gzip needs DecompressionStream, which this browser lacks");
}
