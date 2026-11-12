import { useEffect, useRef } from "react";
import { reducedMotion } from "./motion";

// Drifting ridgelines behind the hero. Decorative: drawn once at rest, and
// animated only while on screen and when motion is allowed.
export default function Terrain() {
  const ref = useRef<HTMLCanvasElement>(null);
  useEffect(() => {
    const cv = ref.current;
    const cx = cv?.getContext("2d");
    if (!cv || !cx) return;
    let W = 0, H = 0;
    const size = () => {
      const dpr = Math.min(devicePixelRatio || 1, 2);
      W = cv.clientWidth;
      H = cv.clientHeight;
      cv.width = W * dpr;
      cv.height = H * dpr;
      cx.setTransform(dpr, 0, 0, dpr, 0, 0);
    };
    const ridge = (x: number, z: number, t: number) =>
      Math.sin(x * 0.0042 + z * 1.7 + t * 0.25) + 0.55 * Math.sin(x * 0.011 + z * 3.1 + 1.3 - t * 0.18) + 0.25 * Math.sin(x * 0.027 + z * 5.3 + t * 0.4);
    const draw = (t: number) => {
      cx.clearRect(0, 0, W, H);
      const g = cx.createRadialGradient(W * 0.62, H * 0.3, 0, W * 0.62, H * 0.3, W * 0.55);
      g.addColorStop(0, "rgba(245,158,11,.20)");
      g.addColorStop(0.45, "rgba(41,213,165,.07)");
      g.addColorStop(1, "rgba(6,10,16,0)");
      cx.fillStyle = g;
      cx.fillRect(0, 0, W, H);
      const rows = 30;
      for (let i = 0; i < rows; i++) {
        const p = i / (rows - 1), y0 = H * (0.3 + 0.72 * p * p), amp = 6 + 70 * p;
        cx.beginPath();
        cx.moveTo(0, H);
        for (let x = 0; x <= W + 10; x += 10) {
          const side = Math.abs(x - W * 0.5) / (W * 0.5), valley = 0.18 + 0.82 * side * side;
          cx.lineTo(x, y0 - (ridge(x * (1.5 - p * 0.8) + t * 18 * (0.3 + p), i * 0.9, t) + 1.2) * amp * valley * 0.5);
        }
        cx.lineTo(W, H);
        cx.closePath();
        cx.fillStyle = "#060a10";
        cx.fill();
        cx.strokeStyle = `rgba(41,213,165,${0.05 + 0.3 * p})`;
        cx.lineWidth = 1;
        cx.stroke();
      }
    };
    let t = 0;
    const onResize = () => {
      size();
      draw(t);
    };
    onResize();
    addEventListener("resize", onResize);
    if (reducedMotion() || typeof IntersectionObserver === "undefined") return () => removeEventListener("resize", onResize);

    let raf = 0, last = 0;
    const tick = (now: number) => {
      t += Math.min(now - last, 100) / 1000;
      last = now;
      draw(t);
      raf = requestAnimationFrame(tick);
    };
    const io = new IntersectionObserver(([e]) => {
      cancelAnimationFrame(raf);
      if (e.isIntersecting) {
        last = performance.now();
        raf = requestAnimationFrame(tick);
      }
    });
    io.observe(cv);
    return () => {
      io.disconnect();
      cancelAnimationFrame(raf);
      removeEventListener("resize", onResize);
    };
  }, []);
  return <canvas ref={ref} className="lp-terrain" aria-hidden="true" />;
}
