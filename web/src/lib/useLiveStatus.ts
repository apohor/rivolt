// useLiveStatus subscribes to /api/live/ws and yields the most recent
// "status" event from the machine plus connection state. Suitable for
// dashboards that just need the latest sensor snapshot, not a full
// chart history. The Hub fan-out lets multiple consumers share a single
// upstream connection.
import { useEffect, useState } from "react";

export type MachineSensors = {
  p: number; // pressure (bar)
  f: number; // flow (g/s)
  w: number; // weight (g)
  t: number; // temperature (°C)
  g: number; // motor position / piston (varies by firmware)
};

export type MachineLiveStatus = {
  name: string;
  sensors: MachineSensors;
  time: number;
  profile: string;
  profile_time: number;
  state: string;
  extracting: boolean;
  loaded_profile: string;
  id: string;
};

type LiveConnState = {
  connected: boolean;
  last_connect: string;
  last_error?: string;
  machine_url: string;
};

type Frame = {
  type: "state" | "event" | "ping";
  name?: string;
  data?: unknown;
  state?: LiveConnState;
};

function wsURL(path: string) {
  const scheme = location.protocol === "https:" ? "wss:" : "ws:";
  return `${scheme}//${location.host}${path}`;
}

export function useLiveStatus() {
  const [status, setStatus] = useState<MachineLiveStatus | null>(null);
  const [conn, setConn] = useState<LiveConnState | null>(null);
  const [connected, setConnected] = useState(false);
  const [lastUpdate, setLastUpdate] = useState<number>(0);

  useEffect(() => {
    let closed = false;
    let ws: WebSocket | null = null;
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null;

    const open = () => {
      if (closed) return;
      ws = new WebSocket(wsURL("/api/live/ws"));
      ws.onopen = () => setConnected(true);
      ws.onclose = () => {
        if (closed) return;
        setConnected(false);
        // Auto-reconnect with a small delay so the home page doesn't
        // get stuck on a stale "disconnected" state if the user leaves
        // and comes back.
        reconnectTimer = setTimeout(open, 2000);
      };
      ws.onerror = () => setConnected(false);
      ws.onmessage = (ev) => {
        let frame: Frame;
        try {
          frame = JSON.parse(ev.data);
        } catch {
          return;
        }
        if (frame.type === "state" && frame.state) {
          setConn(frame.state);
          return;
        }
        if (frame.type === "event" && frame.name === "status" && frame.data) {
          setStatus(frame.data as MachineLiveStatus);
          setLastUpdate(Date.now());
        }
      };
    };

    open();
    return () => {
      closed = true;
      if (reconnectTimer) clearTimeout(reconnectTimer);
      ws?.close();
    };
  }, []);

  return { status, conn, connected, lastUpdate };
}
