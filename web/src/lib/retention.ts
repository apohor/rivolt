// Second-by-second detail is kept for a limited time (older drives and charges keep their figures, and a drive its
// route). A session older than this with no samples is read as "no longer kept", not "never recorded".
export const DETAIL_KEPT_DAYS = 90;

export const detailPastKeeping = (startedAt: string, now: Date = new Date()): boolean => {
  const t = new Date(startedAt).getTime();
  return Number.isFinite(t) && now.getTime() - t > DETAIL_KEPT_DAYS * 86_400_000;
};
